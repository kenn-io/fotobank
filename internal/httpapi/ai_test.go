package httpapi_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"go.kenn.io/fotobank/internal/ai"
	"go.kenn.io/fotobank/internal/ai/ack"
	"go.kenn.io/fotobank/internal/ai/failures"
	"go.kenn.io/fotobank/internal/ai/gapscanner"
	"go.kenn.io/fotobank/internal/ai/jobs"
	"go.kenn.io/fotobank/internal/ai/parse"
	"go.kenn.io/fotobank/internal/ai/results"
	"go.kenn.io/fotobank/internal/ai/skipped"
	"go.kenn.io/fotobank/internal/errs"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/identity"
	"go.kenn.io/fotobank/internal/owners"
	aiservice "go.kenn.io/fotobank/internal/service/ai"
	"go.kenn.io/fotobank/internal/testutil"
)

type unreachableProbe struct{}

func (unreachableProbe) Probe(_ context.Context) error { return errors.New("unreachable") }

type aiAPIFixture struct {
	srv        *httptest.Server
	owner      owners.Principal
	svc        *aiservice.Service
	rw         *sql.DB
	resR       *results.Repo
	failR      *failures.Repo
	skipR      *skipped.Repo
	mediaCheck *aiTestMediaCheck
}

// aiTestMediaCheck is the gate plumbed into AIService.Deps in tests.
// It mirrors MediaService.Get's contract (errs.ErrNotFound on cross-
// owner or locked-hidden reads); production wiring uses the real
// MediaService. Tests register media via add().
type aiTestMediaCheck struct {
	rows map[string]struct {
		owner  owners.Principal
		hidden bool
	}
}

func newAITestMediaCheck() *aiTestMediaCheck {
	return &aiTestMediaCheck{rows: map[string]struct {
		owner  owners.Principal
		hidden bool
	}{}}
}

func (m *aiTestMediaCheck) add(id string, owner owners.Principal, hidden bool) {
	m.rows[id] = struct {
		owner  owners.Principal
		hidden bool
	}{owner: owner, hidden: hidden}
}

func (m *aiTestMediaCheck) Check(_ context.Context, id string, caller owners.Principal, includeHidden bool) error {
	row, ok := m.rows[id]
	if !ok {
		return errs.ErrNotFound
	}
	if row.owner != caller {
		return errs.ErrNotFound
	}
	if row.hidden && !includeHidden {
		return errs.ErrNotFound
	}
	return nil
}

func newAIAPIFixture(t *testing.T) aiAPIFixture {
	t.Helper()
	d := testutil.OpenTestDB(t)
	rw, ro := d.WriteDB(), d.ReadDB()
	owner := owners.Principal{Hub: "local", UserID: "alice"}
	_, err := rw.ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, "550e8400-e29b-41d4-a716-446655440000", time.Now().UTC(),
	)
	require.NoError(t, err)

	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	failR := failures.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	ackS := ack.New(rw, ro)
	gs := gapscanner.New(ro, q, resR, skipR)
	mediaCheck := newAITestMediaCheck()
	svc := aiservice.New(aiservice.Deps{
		Queue:    q,
		Results:  resR,
		Failures: failR,
		Skipped:  skipR,
		Ack:      ackS,
		Gap:      gs,
		Media:    mediaCheck,
		ConfigFingerprints: aiservice.ConfigFingerprints{
			Tag:     ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"},
			Caption: ai.Fingerprint{ModelID: "m", PromptVersion: "caption-v1", InputProfile: "ip"},
		},
	})

	idp := identity.NewStub(owner, "Test User")
	h, err := httpapi.New(httpapi.Deps{
		IdentityProvider: idp,
		AIService:        svc,
		AIVisionProbe:    unreachableProbe{},
		AIEnabled:        true,
	})
	require.NoError(t, err)
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return aiAPIFixture{
		srv: srv, owner: owner, svc: svc, rw: rw,
		resR: resR, failR: failR, skipR: skipR, mediaCheck: mediaCheck,
	}
}

func TestAIHealthReportsAcknowledgementRequired(t *testing.T) {
	require := require.New(t)
	fx := newAIAPIFixture(t)

	resp, err := fx.srv.Client().Get(fx.srv.URL + "/api/v1/ai/health")
	require.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(err)
	require.Contains(string(body), `"paused_reason":"acknowledgement_required"`)
}

func TestAIBackfillRequiresAcknowledgement(t *testing.T) {
	require := require.New(t)
	fx := newAIAPIFixture(t)

	resp, err := fx.srv.Client().Post(fx.srv.URL+"/api/v1/ai/backfill",
		"application/json",
		strings.NewReader(`{"task":"tag","scope":"all"}`))
	require.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(http.StatusConflict, resp.StatusCode)
}

func TestAIAcknowledgeRecordsAck(t *testing.T) {
	require := require.New(t)
	fx := newAIAPIFixture(t)

	resp, err := fx.srv.Client().Post(fx.srv.URL+"/api/v1/ai/acknowledge",
		"application/json",
		strings.NewReader(`{"kind":"hidden_processing"}`))
	require.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(http.StatusOK, resp.StatusCode)

	acked, err := fx.svc.IsAcknowledged(context.Background(), fx.owner)
	require.NoError(err)
	require.True(acked)
}

func TestAIMediaViewReturnsArtifacts(t *testing.T) {
	r := require.New(t)
	fx := newAIAPIFixture(t)
	mid := testutil.SeedPhoto(t, fx.rw, fx.owner, "p1")
	fx.mediaCheck.add(mid, fx.owner, false)

	tagFP := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	captionFP := ai.Fingerprint{ModelID: "m", PromptVersion: "caption-v1", InputProfile: "ip"}
	ctx := context.Background()
	r.NoError(fx.resR.WriteTagResult(ctx, mid, tagFP, "thash", []parse.Tag{
		{Key: "dog", Label: "Dog", Rank: 1},
	}))
	r.NoError(fx.resR.WriteCaptionResult(ctx, mid, captionFP, "chash", "A small dog."))
	r.NoError(fx.failR.Record(ctx, mid, ai.TaskCaption, captionFP, ai.ErrKindMalformed, "bad json", 2))

	resp, err := fx.srv.Client().Get(fx.srv.URL + "/api/v1/media/" + mid + "/ai")
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusOK, resp.StatusCode)

	var body aiservice.MediaView
	r.NoError(json.NewDecoder(resp.Body).Decode(&body))
	r.Len(body.Tags, 1)
	r.Equal("Dog", body.Tags[0].Label)
	r.NotNil(body.Caption)
	r.Equal("A small dog.", body.Caption.Text)
	r.NotNil(body.CaptionFailure)
	r.Equal("bad json", body.CaptionFailure.Message)
	r.Nil(body.TagFailure)
}

// TestAIMediaViewReturns404ForUnknownMedia verifies the High-severity
// fix: an unknown media id returns 404, not 200 + empty body. Without
// the gate, an empty MediaView would leak the existence of media that
// the caller does not own (and would leak hidden rows when tags happen
// to exist independently of the visibility check).
func TestAIMediaViewReturns404ForUnknownMedia(t *testing.T) {
	r := require.New(t)
	fx := newAIAPIFixture(t)

	resp, err := fx.srv.Client().Get(fx.srv.URL + "/api/v1/media/missing/ai")
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusNotFound, resp.StatusCode)
}

// TestAIMediaViewReturns404ForCrossOwner verifies the High-severity
// fix: even with a seeded result, a media owned by another principal
// is invisible to the caller (anti-enumeration).
func TestAIMediaViewReturns404ForCrossOwner(t *testing.T) {
	r := require.New(t)
	fx := newAIAPIFixture(t)
	bob := testutil.SeedOwner(t, fx.rw, "local", "bob")
	bobMid := testutil.SeedPhoto(t, fx.rw, bob, "b1")
	fx.mediaCheck.add(bobMid, bob, false)

	tagFP := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	r.NoError(fx.resR.WriteTagResult(context.Background(), bobMid, tagFP, "h",
		[]parse.Tag{{Key: "x", Label: "x", Rank: 1}}))

	// Caller is Alice (per the fixture's identity stub); requesting
	// Bob's media must 404.
	resp, err := fx.srv.Client().Get(fx.srv.URL + "/api/v1/media/" + bobMid + "/ai")
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusNotFound, resp.StatusCode)
}

// TestAIMediaViewReturns404ForLockedHidden verifies the second half of
// the High finding: a hidden row owned by the caller is invisible to
// /api/v1/media/{id}/ai without an unlock cookie. The fixture does not
// set an unlock claim, so the gate must hide the row.
func TestAIMediaViewReturns404ForLockedHidden(t *testing.T) {
	r := require.New(t)
	fx := newAIAPIFixture(t)
	mid := testutil.SeedPhoto(t, fx.rw, fx.owner, "p-hidden")
	fx.mediaCheck.add(mid, fx.owner, true) // hidden

	tagFP := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	r.NoError(fx.resR.WriteTagResult(context.Background(), mid, tagFP, "h",
		[]parse.Tag{{Key: "secret", Label: "secret", Rank: 1}}))

	resp, err := fx.srv.Client().Get(fx.srv.URL + "/api/v1/media/" + mid + "/ai")
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusNotFound, resp.StatusCode)
}

func TestAIMediaViewSurfacesSkipReason(t *testing.T) {
	r := require.New(t)
	fx := newAIAPIFixture(t)
	mid := testutil.SeedPhoto(t, fx.rw, fx.owner, "p1")
	fx.mediaCheck.add(mid, fx.owner, false)
	r.NoError(fx.skipR.Record(context.Background(), mid, ai.TaskTag, "video"))

	resp, err := fx.srv.Client().Get(fx.srv.URL + "/api/v1/media/" + mid + "/ai")
	r.NoError(err)
	defer func() { _ = resp.Body.Close() }()
	r.Equal(http.StatusOK, resp.StatusCode)

	var body aiservice.MediaView
	r.NoError(json.NewDecoder(resp.Body).Decode(&body))
	r.NotNil(body.Skipped)
	r.Equal("video", body.Skipped.Reason)
}
