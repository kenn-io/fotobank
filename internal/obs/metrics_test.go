package obs

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewMetrics_BuildInfoEmitted(t *testing.T) {
	r := require.New(t)
	m := NewMetrics(MetricSources{}, BuildInfo{
		Version: "v1.2.3", Commit: "abc1234", BuildDate: "2026-04-25",
	})
	var buf bytes.Buffer
	m.WritePrometheus(&buf)
	out := buf.String()
	r.Contains(out, `fotobank_build_info{`)
	r.Contains(out, `version="v1.2.3"`)
	r.Contains(out, `commit="abc1234"`)
	r.Contains(out, `build_date="2026-04-25"`)
}

func TestNewMetrics_HTTPCounterIncrements(t *testing.T) {
	m := NewTestMetrics()
	m.HTTPRequests("GET", "/api/v1/healthz", "2xx").Inc()
	m.HTTPRequests("GET", "/api/v1/healthz", "2xx").Inc()
	m.HTTPRequests("POST", "/api/v1/albums", "4xx").Inc()
	require.EqualValues(t, 2, m.HTTPRequests("GET", "/api/v1/healthz", "2xx").Get())
	require.EqualValues(t, 1, m.HTTPRequests("POST", "/api/v1/albums", "4xx").Get())
}

func TestNewMetrics_HTTPDurationObserved(t *testing.T) {
	m := NewTestMetrics()
	m.HTTPRequestDuration("GET", "/api/v1/healthz").Update(0.012)
	m.HTTPRequestDuration("GET", "/api/v1/healthz").Update(0.007)

	var buf bytes.Buffer
	m.WritePrometheus(&buf)
	out := buf.String()
	require.Contains(t, out, `fotobank_http_request_duration_seconds_count{method="GET",route="/api/v1/healthz"} 2`)
	require.Contains(t, out, `fotobank_http_request_duration_seconds_bucket{`)
	require.Contains(t, out, `le="0.025"`)
}

func TestNewMetrics_WorkerAccessors(t *testing.T) {
	r := require.New(t)
	m := NewTestMetrics()
	m.ThumbJobs("ok").Inc()
	m.ThumbJobs("failed").Inc()
	m.ThumbLeasesSwept().Add(3)
	m.SharePublishes("retry").Inc()
	m.ShareRevokes("ok").Inc()
	m.BackupSnapshots("ok").Inc()
	m.BackupRetentionDeleted().Add(5)

	r.EqualValues(1, m.ThumbJobs("ok").Get())
	r.EqualValues(1, m.ThumbJobs("failed").Get())
	r.EqualValues(3, m.ThumbLeasesSwept().Get())
	r.EqualValues(1, m.SharePublishes("retry").Get())
	r.EqualValues(1, m.ShareRevokes("ok").Get())
	r.EqualValues(1, m.BackupSnapshots("ok").Get())
	r.EqualValues(5, m.BackupRetentionDeleted().Get())
}

func TestNewMetrics_BackupLastSuccessPushed(t *testing.T) {
	r := require.New(t)
	m := NewTestMetrics()
	var buf bytes.Buffer
	m.WritePrometheus(&buf)
	r.Contains(buf.String(), `fotobank_backup_seconds_since_last_success -1`)
	r.Contains(buf.String(), `fotobank_backup_last_success_unix 0`)

	m.SetBackupLastSuccess(1_700_000_000)
	buf.Reset()
	m.WritePrometheus(&buf)
	out := buf.String()
	// VictoriaMetrics renders integer-valued gauges without scientific notation.
	r.Contains(out, `fotobank_backup_last_success_unix 1700000000`)
	r.NotContains(out, `fotobank_backup_seconds_since_last_success -1`)
}

func TestNewMetrics_ProcessMetricsAppended(t *testing.T) {
	m := NewTestMetrics()
	var buf bytes.Buffer
	m.WritePrometheus(&buf)
	out := buf.String()
	require.Contains(t, out, "go_goroutines")
	require.True(t, strings.Contains(out, "go_memstats") || strings.Contains(out, "process_"),
		"expected at least one go_memstats_* or process_* series; got %q", out)
}

func TestNewMetrics_AIMetricsExposed(t *testing.T) {
	r := require.New(t)
	m := NewMetrics(MetricSources{
		AIJobsDepth: func(task, status string) int64 { return 7 },
	}, BuildInfo{})

	m.AIJobs("tag", "ok").Inc()
	m.AIJobs("caption", "failed").Inc()
	m.AIRequestDuration("tag", "ok").Update(0.12)
	m.AIRequestDuration("caption", "transient").Update(2.5)
	m.SetAIVisionReachable(true)
	m.SetAIAcknowledgementRequired(false)

	var buf bytes.Buffer
	m.WritePrometheus(&buf)
	out := buf.String()

	r.Contains(out, `fotobank_ai_jobs_depth`)
	r.Contains(out, `fotobank_ai_jobs_completed_total`)
	r.Contains(out, `fotobank_ai_request_duration_seconds`)
	r.Contains(out, `fotobank_ai_endpoint_reachable`)
	r.Contains(out, `fotobank_ai_acknowledgement_required`)

	r.Contains(out, `fotobank_ai_jobs_depth{task="tag",status="pending"} 7`)
	r.Contains(out, `fotobank_ai_jobs_completed_total{task="tag",result="ok"} 1`)
	r.Contains(out, `fotobank_ai_jobs_completed_total{task="caption",result="failed"} 1`)
	r.Contains(out, `fotobank_ai_endpoint_reachable{kind="vision"} 1`)
	r.Contains(out, `fotobank_ai_acknowledgement_required 0`)
}

// TestMetrics_AIJobsDepthEmbedLabel verifies the AIJobsDepth pull-source
// gauge accepts task="embed" alongside the existing "tag" / "caption"
// labels (R1 plan: "Existing AI metrics extend with task=\"embed\""
// — the label set already accepts arbitrary string values, but the
// boot-time loop has to register the gauge so a closure exists for the
// scrape side to invoke).
func TestMetrics_AIJobsDepthEmbedLabel(t *testing.T) {
	r := require.New(t)
	m := NewMetrics(MetricSources{
		AIJobsDepth: func(task, status string) int64 {
			if task == "embed" && status == "pending" {
				return 42
			}
			return 0
		},
	}, BuildInfo{})

	var buf bytes.Buffer
	m.WritePrometheus(&buf)
	out := buf.String()

	// Embed task is registered for all three statuses; the closure
	// returns the canned 42 for {embed, pending} only, and zero for the
	// other two — both flavours assert the label gauge exists.
	r.Contains(out, `fotobank_ai_jobs_depth{task="embed",status="pending"} 42`)
	r.Contains(out, `fotobank_ai_jobs_depth{task="embed",status="working"} 0`)
	r.Contains(out, `fotobank_ai_jobs_depth{task="embed",status="blocked"} 0`)
}

// TestMetrics_SearchRequestsTotal exercises the search-request counter
// at multiple (mode, sort) label combos so a regression that drops
// either dimension lights up. The labels come from the engine's
// post-coercion mode and effective sort.
func TestMetrics_SearchRequestsTotal(t *testing.T) {
	r := require.New(t)
	m := NewTestMetrics()

	// Three increments across distinct (mode, sort) tuples so the
	// counter assertion can distinguish per-tuple state.
	m.SearchRequests("hybrid", "relevance").Inc()
	m.SearchRequests("hybrid", "relevance").Inc()
	m.SearchRequests("bm25_only", "newest").Inc()
	m.SearchRequests("filter_only", "oldest").Inc()

	r.EqualValues(2, m.SearchRequests("hybrid", "relevance").Get())
	r.EqualValues(1, m.SearchRequests("bm25_only", "newest").Get())
	r.EqualValues(1, m.SearchRequests("filter_only", "oldest").Get())

	var buf bytes.Buffer
	m.WritePrometheus(&buf)
	out := buf.String()
	r.Contains(out, `fotobank_search_requests_total{mode="hybrid",sort="relevance"} 2`)
	r.Contains(out, `fotobank_search_requests_total{mode="bm25_only",sort="newest"} 1`)
	r.Contains(out, `fotobank_search_requests_total{mode="filter_only",sort="oldest"} 1`)
}

func TestNewMetrics_PullSourceClosuresArePerState(t *testing.T) {
	r := require.New(t)
	thumbCalls := make(map[string]int)
	shareCalls := make(map[string]int)

	m := NewMetrics(MetricSources{
		ThumbQueueDepth: func(state string) int64 {
			thumbCalls[state]++
			switch state {
			case "pending":
				return 1
			case "working":
				return 2
			case "failed":
				return 3
			case "no_preview":
				return 4
			}
			return 0
		},
		SharePendingByOp: func(op string) int64 {
			shareCalls[op]++
			switch op {
			case "publish":
				return 11
			case "revoke":
				return 22
			}
			return 0
		},
	}, BuildInfo{})

	var buf bytes.Buffer
	m.WritePrometheus(&buf)
	out := buf.String()

	// Each labeled gauge must invoke its closure with the correct state/op.
	r.Equal(1, thumbCalls["pending"], "closure for state=pending fired %d times, want 1", thumbCalls["pending"])
	r.Equal(1, thumbCalls["working"])
	r.Equal(1, thumbCalls["failed"])
	r.Equal(1, thumbCalls["no_preview"])
	r.Equal(1, shareCalls["publish"])
	r.Equal(1, shareCalls["revoke"])

	// Output reflects each closure's distinct return value (proves the
	// captured state/op variables don't all alias the loop's last value).
	r.Contains(out, `fotobank_thumb_queue_depth{state="pending"} 1`)
	r.Contains(out, `fotobank_thumb_queue_depth{state="working"} 2`)
	r.Contains(out, `fotobank_thumb_queue_depth{state="failed"} 3`)
	r.Contains(out, `fotobank_thumb_queue_depth{state="no_preview"} 4`)
	r.Contains(out, `fotobank_share_pending_total{op="publish"} 11`)
	r.Contains(out, `fotobank_share_pending_total{op="revoke"} 22`)
}
