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
