package worker_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/wesm/fotobank/internal/ai"
	"github.com/wesm/fotobank/internal/ai/ack"
	"github.com/wesm/fotobank/internal/ai/failures"
	"github.com/wesm/fotobank/internal/ai/gateway"
	"github.com/wesm/fotobank/internal/ai/jobs"
	"github.com/wesm/fotobank/internal/ai/results"
	"github.com/wesm/fotobank/internal/ai/skipped"
	"github.com/wesm/fotobank/internal/ai/worker"
	"github.com/wesm/fotobank/internal/owners"
	"github.com/wesm/fotobank/internal/testutil"
)

type stubGateway struct {
	respond func() (gateway.Response, error)
	calls   atomic.Int32
}

func (s *stubGateway) Generate(_ context.Context, _ gateway.Request) (gateway.Response, error) {
	s.calls.Add(1)
	return s.respond()
}

func (s *stubGateway) HealthCheck(_ context.Context) error { return nil }

type stubImage struct {
	jpeg   []byte
	status string
	err    error
}

func (s *stubImage) ResolveAndEncode(_ context.Context, _ string) ([]byte, string, error) {
	return s.jpeg, s.status, s.err
}

func setup(t *testing.T) (
	*worker.Worker, *jobs.Queue, *results.Repo, *failures.Repo, *skipped.Repo,
	owners.Principal, string, *stubGateway, *stubImage,
) {
	t.Helper()
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")

	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	failR := failures.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	ackS := ack.New(rw, ro)
	require.NoError(t, ackS.Acknowledge(context.Background(), owner))

	gw := &stubGateway{}
	img := &stubImage{jpeg: []byte{0xff, 0xd8, 0xff, 0xd9}, status: "ready"}

	w := worker.New(worker.Config{
		Task:        ai.TaskTag,
		Fingerprint: ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"},
		PromptHash:  "h",
		PromptText:  "describe",
		Gateway:     gw,
		Image:       img,
		Queue:       q,
		Results:     resR,
		Failures:    failR,
		Skipped:     skipR,
		Acknowledged: func(ctx context.Context, _ owners.Principal) (bool, error) {
			return ackS.IsAcknowledged(ctx, owner)
		},
		OwnerOf: func(_ context.Context, _ string) (owners.Principal, error) {
			return owner, nil
		},
		MaxJobAttempts: 2,
		Process:        worker.TagProcess,
		Sem:            worker.NewVisionSemaphore(1),
	})

	return w, q, resR, failR, skipR, owner, mid, gw, img
}

func TestWorkerProcessesSuccessfulTagJob(t *testing.T) {
	r := require.New(t)
	w, q, resR, _, _, _, mid, gw, _ := setup(t)
	gw.respond = func() (gateway.Response, error) {
		return gateway.Response{Text: `{"tags":["dog","beach"]}`}, nil
	}
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	r.NoError(q.Enqueue(context.Background(), mid, ai.TaskTag, fp))

	n, err := w.RunOnce(context.Background())
	r.NoError(err)
	r.Equal(1, n)
	tags, err := resR.GetActiveTags(context.Background(), mid)
	r.NoError(err)
	r.Len(tags, 2)
}

func TestWorkerMalformedRetriesThenFails(t *testing.T) {
	r := require.New(t)
	w, q, _, failR, _, _, mid, gw, _ := setup(t)
	gw.respond = func() (gateway.Response, error) {
		return gateway.Response{Text: "totally not json"}, nil
	}
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	r.NoError(q.Enqueue(context.Background(), mid, ai.TaskTag, fp))

	n, err := w.RunOnce(context.Background())
	r.NoError(err)
	r.Equal(1, n)
	n, err = w.RunOnce(context.Background())
	r.NoError(err)
	r.Equal(1, n)

	cnt, err := failR.CountForFingerprint(context.Background(), ai.TaskTag, fp)
	r.NoError(err)
	r.Equal(1, cnt)
}

func TestWorker4xxFailsImmediately(t *testing.T) {
	r := require.New(t)
	w, q, _, failR, _, _, mid, gw, _ := setup(t)
	gw.respond = func() (gateway.Response, error) {
		return gateway.Response{}, fmt.Errorf("HTTP 400 image too large: %w", gateway.ErrPermanent4xx)
	}
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	r.NoError(q.Enqueue(context.Background(), mid, ai.TaskTag, fp))

	n, err := w.RunOnce(context.Background())
	r.NoError(err)
	r.Equal(1, n)
	r.EqualValues(1, gw.calls.Load(), "no retry on 4xx")
	cnt, err := failR.CountForFingerprint(context.Background(), ai.TaskTag, fp)
	r.NoError(err)
	r.Equal(1, cnt)
}

func TestWorkerSkipsOnNoPreview(t *testing.T) {
	r := require.New(t)
	w, q, _, _, skipR, _, mid, _, img := setup(t)
	img.status = "no_preview"
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	r.NoError(q.Enqueue(context.Background(), mid, ai.TaskTag, fp))

	_, err := w.RunOnce(context.Background())
	r.NoError(err)
	reason, found, err := skipR.Get(context.Background(), mid, ai.TaskTag)
	r.NoError(err)
	r.True(found)
	r.Equal("no_preview", reason)
}

func TestWorkerBlocksOnPendingThumb(t *testing.T) {
	r := require.New(t)
	w, q, _, _, _, _, mid, _, img := setup(t)
	img.status = "pending"
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	r.NoError(q.Enqueue(context.Background(), mid, ai.TaskTag, fp))

	_, err := w.RunOnce(context.Background())
	r.NoError(err)
	c, err := q.Counters(context.Background(), ai.TaskTag)
	r.NoError(err)
	r.Equal(1, c.Blocked)
}

func TestWorkerParkedWithoutAcknowledgement(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	q := jobs.NewQueue(rw, ro)
	gw := &stubGateway{respond: func() (gateway.Response, error) {
		return gateway.Response{Text: `{"tags":["x"]}`}, nil
	}}
	img := &stubImage{jpeg: []byte{0xff, 0xd8, 0xff, 0xd9}, status: "ready"}
	w := worker.New(worker.Config{
		Task:        ai.TaskTag,
		Fingerprint: ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"},
		PromptHash:  "h",
		PromptText:  "describe",
		Gateway:     gw, Image: img,
		Queue: q, Results: results.NewRepo(rw, ro),
		Failures: failures.NewRepo(rw, ro), Skipped: skipped.NewRepo(rw, ro),
		Acknowledged: func(_ context.Context, _ owners.Principal) (bool, error) { return false, nil },
		OwnerOf: func(_ context.Context, _ string) (owners.Principal, error) {
			return owner, nil
		},
		MaxJobAttempts: 2, Process: worker.TagProcess,
		Sem: worker.NewVisionSemaphore(1),
	})
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	r.NoError(q.Enqueue(context.Background(), mid, ai.TaskTag, fp))

	// The worker DID claim the job (RunOnce returns processed count, including
	// released). What matters is the gateway was NOT called.
	_, err := w.RunOnce(context.Background())
	r.NoError(err)
	r.EqualValues(0, gw.calls.Load())
}
