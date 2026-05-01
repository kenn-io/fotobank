package httpapi_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/ack"
	"github.com/wesm/fotobank/internal/ai/failures"
	"github.com/wesm/fotobank/internal/ai/gapscanner"
	"github.com/wesm/fotobank/internal/ai/jobs"
	"github.com/wesm/fotobank/internal/ai/results"
	"github.com/wesm/fotobank/internal/ai/skipped"
	"github.com/wesm/fotobank/internal/httpapi"
	"github.com/wesm/fotobank/internal/identity"
	"github.com/wesm/fotobank/internal/owners"
	aiservice "github.com/wesm/fotobank/internal/service/ai"
	"github.com/wesm/fotobank/internal/testutil"
)

type unreachableProbe struct{}

func (unreachableProbe) Probe(_ context.Context) error { return errors.New("unreachable") }

type aiAPIFixture struct {
	srv   *httptest.Server
	owner owners.Principal
	svc   *aiservice.Service
}

func newAIAPIFixture(t *testing.T) aiAPIFixture {
	t.Helper()
	d := testutil.OpenTestDB(t)
	rw, ro := d.WriteDB(), d.ReadDB()
	owner := owners.Principal{Hub: "local", UserID: "alice"}
	_, err := rw.ExecContext(context.Background(),
		`INSERT INTO owners(hub, user_id, storage_key, created_at) VALUES(?,?,?,?)`,
		owner.Hub, owner.UserID, owner.Hub+"/"+owner.UserID, time.Now().UTC(),
	)
	require.NoError(t, err)

	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	failR := failures.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	ackS := ack.New(rw, ro)
	gs := gapscanner.New(ro, q, resR, skipR)
	svc := aiservice.New(aiservice.Deps{
		Queue:    q,
		Results:  resR,
		Failures: failR,
		Skipped:  skipR,
		Ack:      ackS,
		Gap:      gs,
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
	return aiAPIFixture{srv: srv, owner: owner, svc: svc}
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
