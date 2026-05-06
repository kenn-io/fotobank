package worker_test

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
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

func (s *stubImage) ResolvePreviewJPEG(_ context.Context, _ string) ([]byte, string, error) {
	return s.jpeg, s.status, s.err
}

type imageSequence struct {
	calls atomic.Int32
	steps []stubImage
}

func (s *imageSequence) ResolvePreviewJPEG(_ context.Context, _ string) ([]byte, string, error) {
	i := int(s.calls.Add(1)) - 1
	if i >= len(s.steps) {
		i = len(s.steps) - 1
	}
	step := s.steps[i]
	return step.jpeg, step.status, step.err
}

// tinyJPEG is a real decodable JPEG produced once at package init. The
// worker re-encodes preview bytes via encode.EncodeChat before calling
// the gateway, so stubs returning a "ready" preview must return bytes
// the JPEG decoder accepts.
var tinyJPEG = func() []byte {
	img := image.NewRGBA(image.Rect(0, 0, 32, 24))
	for y := range 24 {
		for x := range 32 {
			img.Set(x, y, color.RGBA{R: uint8(x * 8), G: uint8(y * 10), B: 0x40, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		panic(fmt.Errorf("seed tinyJPEG: %w", err))
	}
	return buf.Bytes()
}()

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
	img := &stubImage{jpeg: tinyJPEG, status: "ready"}

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

func TestWorkerClaimsOnlyCurrentClaimFingerprint(t *testing.T) {
	tests := []struct {
		name     string
		task     ai.Task
		process  worker.ProcessFn
		response string
	}{
		{name: "tag", task: ai.TaskTag, process: worker.TagProcess, response: `{"tags":["dog"]}`},
		{name: "caption", task: ai.TaskCaption, process: worker.CaptionProcess, response: `a small dog on a beach`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := require.New(t)
			rw, ro := testutil.OpenTestDBPair(t)
			owner := testutil.SeedOwner(t, rw, "local", "alice")
			oldID := testutil.SeedPhoto(t, rw, owner, "old")
			newID := testutil.SeedPhoto(t, rw, owner, "new")
			q := jobs.NewQueue(rw, ro)
			gw := &stubGateway{respond: func() (gateway.Response, error) {
				return gateway.Response{Text: tt.response}, nil
			}}
			w := worker.New(worker.Config{
				Task:             tt.task,
				ClaimFingerprint: "claim-current",
				Fingerprint:      ai.Fingerprint{ModelID: "m", PromptVersion: "prompt-v1", InputProfile: "ip"},
				PromptHash:       "h",
				PromptText:       "describe",
				Gateway:          gw,
				Image:            &stubImage{jpeg: tinyJPEG, status: "ready"},
				Queue:            q,
				Results:          results.NewRepo(rw, ro),
				Failures:         failures.NewRepo(rw, ro),
				Skipped:          skipped.NewRepo(rw, ro),
				Acknowledged: func(context.Context, owners.Principal) (bool, error) {
					return true, nil
				},
				OwnerOf: func(context.Context, string) (owners.Principal, error) {
					return owner, nil
				},
				MaxJobAttempts: 2,
				Process:        tt.process,
				Sem:            worker.NewVisionSemaphore(1),
			})

			r.NoError(q.EnqueueClaim(context.Background(), oldID, tt.task, "claim-old"))
			r.NoError(q.EnqueueClaim(context.Background(), newID, tt.task, "claim-current"))

			n, err := w.RunOnce(context.Background())
			r.NoError(err)
			r.Equal(1, n)
			r.EqualValues(1, gw.calls.Load())

			oldClaims, err := q.ClaimBatchForFingerprint(context.Background(), tt.task, "claim-old", 10)
			r.NoError(err)
			r.Len(oldClaims, 1)
			r.Equal(oldID, oldClaims[0].MediaID)
		})
	}
}

func TestWorkerUsesRuntimeSnapshotForClaimAndResultFingerprint(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "runtime")
	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	gw := &stubGateway{respond: func() (gateway.Response, error) {
		return gateway.Response{Text: `{"tags":["runtime"]}`}, nil
	}}
	resultFP := ai.Fingerprint{ModelID: "runtime-model", PromptVersion: "tags-v1", InputProfile: "ip"}
	w := worker.New(worker.Config{
		Task:        ai.TaskTag,
		Fingerprint: ai.Fingerprint{ModelID: "boot-model", PromptVersion: "tags-v1", InputProfile: "ip"},
		PromptHash:  "h",
		PromptText:  "describe",
		Gateway:     &stubGateway{},
		Image:       &stubImage{jpeg: tinyJPEG, status: "ready"},
		Queue:       q,
		Results:     resR,
		Failures:    failures.NewRepo(rw, ro),
		Skipped:     skipped.NewRepo(rw, ro),
		Acknowledged: func(context.Context, owners.Principal) (bool, error) {
			return true, nil
		},
		OwnerOf: func(context.Context, string) (owners.Principal, error) {
			return owner, nil
		},
		MaxJobAttempts: 2,
		Process:        worker.TagProcess,
		Sem:            worker.NewVisionSemaphore(1),
		Runtime: func(context.Context) worker.RuntimeConfig {
			return worker.RuntimeConfig{
				ClaimFingerprint: "claim-runtime",
				Fingerprint:      resultFP,
				Gateway:          gw,
			}
		},
	})

	r.NoError(q.EnqueueClaim(context.Background(), mid, ai.TaskTag, "claim-runtime"))
	n, err := w.RunOnce(context.Background())
	r.NoError(err)
	r.Equal(1, n)
	r.EqualValues(1, gw.calls.Load())

	done, err := resR.DoneCount(context.Background(), ai.TaskTag, resultFP)
	r.NoError(err)
	r.Equal(1, done)
}

func TestWorkerRuntimeSnapshotCanPauseClaims(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "paused")
	q := jobs.NewQueue(rw, ro)
	gw := &stubGateway{respond: func() (gateway.Response, error) {
		return gateway.Response{Text: `{"tags":["paused"]}`}, nil
	}}
	w := worker.New(worker.Config{
		Task:        ai.TaskTag,
		Fingerprint: ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"},
		PromptHash:  "h",
		PromptText:  "describe",
		Gateway:     gw,
		Image:       &stubImage{jpeg: tinyJPEG, status: "ready"},
		Queue:       q,
		Results:     results.NewRepo(rw, ro),
		Failures:    failures.NewRepo(rw, ro),
		Skipped:     skipped.NewRepo(rw, ro),
		Acknowledged: func(context.Context, owners.Principal) (bool, error) {
			return true, nil
		},
		OwnerOf: func(context.Context, string) (owners.Principal, error) {
			return owner, nil
		},
		MaxJobAttempts: 2,
		Process:        worker.TagProcess,
		Sem:            worker.NewVisionSemaphore(1),
		Runtime: func(context.Context) worker.RuntimeConfig {
			return worker.RuntimeConfig{Disabled: true}
		},
	})

	r.NoError(q.Enqueue(context.Background(), mid, ai.TaskTag, ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}))
	n, err := w.RunOnce(context.Background())
	r.NoError(err)
	r.Equal(0, n)
	r.EqualValues(0, gw.calls.Load())
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

func TestWorkerImageErrorRetriesBeforeFailing(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")

	q := jobs.NewQueue(rw, ro)
	resR := results.NewRepo(rw, ro)
	failR := failures.NewRepo(rw, ro)
	skipR := skipped.NewRepo(rw, ro)
	ackS := ack.New(rw, ro)
	require.NoError(t, ackS.Acknowledge(context.Background(), owner))

	gw := &stubGateway{respond: func() (gateway.Response, error) {
		return gateway.Response{Text: `{"tags":["x"]}`}, nil
	}}
	img := &imageSequence{steps: []stubImage{
		{err: fmt.Errorf("transient io read"), status: ""},
		{err: fmt.Errorf("transient io read"), status: ""},
	}}

	w := worker.New(worker.Config{
		Task:        ai.TaskTag,
		Fingerprint: ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"},
		PromptHash:  "h", PromptText: "describe",
		Gateway: gw, Image: img,
		Queue: q, Results: resR, Failures: failR, Skipped: skipR,
		Acknowledged: func(ctx context.Context, _ owners.Principal) (bool, error) {
			return ackS.IsAcknowledged(ctx, owner)
		},
		OwnerOf: func(_ context.Context, _ string) (owners.Principal, error) {
			return owner, nil
		},
		MaxJobAttempts: 2, Process: worker.TagProcess,
		Sem: worker.NewVisionSemaphore(1),
	})
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	r.NoError(q.Enqueue(context.Background(), mid, ai.TaskTag, fp))

	// First attempt: image err → retryable (back to pending, attempts=1).
	_, err := w.RunOnce(context.Background())
	r.NoError(err)
	cnt, err := failR.CountForFingerprint(context.Background(), ai.TaskTag, fp)
	r.NoError(err)
	r.Equal(0, cnt, "first attempt should not record terminal failure")

	// Second attempt: image err again → terminal failure.
	_, err = w.RunOnce(context.Background())
	r.NoError(err)
	cnt, err = failR.CountForFingerprint(context.Background(), ai.TaskTag, fp)
	r.NoError(err)
	r.Equal(1, cnt, "image error should retry then fail with MissingAIInput")
}

func TestWorkerPromotesAckedBlockedJobs(t *testing.T) {
	r := require.New(t)
	rw, ro := testutil.OpenTestDBPair(t)
	owner := testutil.SeedOwner(t, rw, "local", "alice")
	mid := testutil.SeedPhoto(t, rw, owner, "p1")
	q := jobs.NewQueue(rw, ro)
	ackS := ack.New(rw, ro)
	gw := &stubGateway{respond: func() (gateway.Response, error) {
		return gateway.Response{Text: `{"tags":["x"]}`}, nil
	}}
	img := &stubImage{jpeg: tinyJPEG, status: "ready"}

	ackedRef := atomic.Bool{}
	w := worker.New(worker.Config{
		Task:        ai.TaskTag,
		Fingerprint: ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"},
		PromptHash:  "h", PromptText: "describe",
		Gateway: gw, Image: img,
		Queue: q, Results: results.NewRepo(rw, ro),
		Failures: failures.NewRepo(rw, ro), Skipped: skipped.NewRepo(rw, ro),
		Acknowledged: func(_ context.Context, _ owners.Principal) (bool, error) {
			return ackedRef.Load(), nil
		},
		OwnerOf: func(_ context.Context, _ string) (owners.Principal, error) {
			return owner, nil
		},
		MaxJobAttempts: 2, Process: worker.TagProcess,
		Sem: worker.NewVisionSemaphore(1),
	})
	fp := ai.Fingerprint{ModelID: "m", PromptVersion: "tags-v1", InputProfile: "ip"}
	r.NoError(q.Enqueue(context.Background(), mid, ai.TaskTag, fp))

	// First tick: not acked → blocked.
	_, err := w.RunOnce(context.Background())
	r.NoError(err)
	c, err := q.Counters(context.Background(), ai.TaskTag)
	r.NoError(err)
	r.Equal(1, c.Blocked)

	// Owner acknowledges.
	r.NoError(ackS.Acknowledge(context.Background(), owner))
	ackedRef.Store(true)

	// Next tick: PromoteBlocked elevates the row, claim succeeds, gateway runs.
	_, err = w.RunOnce(context.Background())
	r.NoError(err)
	r.EqualValues(1, gw.calls.Load(), "promoted job should reach the gateway")
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
	img := &stubImage{jpeg: tinyJPEG, status: "ready"}
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
