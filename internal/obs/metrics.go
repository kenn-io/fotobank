package obs

import (
	"io"
	"sync/atomic"
	"time"

	"github.com/VictoriaMetrics/metrics"
)

// BuildInfo carries the build labels emitted by fotobank_build_info.
// Filled from internal/version at boot.
type BuildInfo struct {
	Version   string
	Commit    string
	BuildDate string
}

// MetricSources are pull-side closures invoked at scrape time to fill
// derived gauges. nil entries are tolerated and resolve to zero.
type MetricSources struct {
	ThumbQueueDepth  func(state string) int64
	SharePendingByOp func(op string) int64
}

// Metrics owns a private *metrics.Set, never the upstream global. All
// counters/histograms/gauges fotobank emits live on this set; the
// process metrics from metrics.WriteProcessMetrics are appended at
// scrape time but not registered.
type Metrics struct {
	set *metrics.Set

	// Backup last-success unix seconds — pushed by backup.Worker on
	// each successful Snapshot. Stored atomically so the scrape closure
	// can read consistently.
	lastBackupUnix atomic.Int64
}

// httpDurationBuckets and workerDurationBuckets are the Prometheus le
// buckets pinned by the spec. HTTP traffic skews fast (5ms..10s);
// worker traffic skews slower (50ms..60s).
var (
	httpDurationBuckets   = []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10}
	workerDurationBuckets = []float64{.05, .1, .25, .5, 1, 2.5, 5, 10, 30, 60}
)

// NewMetrics constructs the production metrics registry. BuildInfo is
// emitted as fotobank_build_info{version,commit,build_date}=1; pull
// sources fill derived gauges.
func NewMetrics(src MetricSources, build BuildInfo) *Metrics {
	m := &Metrics{set: metrics.NewSet()}

	// Build info: a constant 1 with build labels.
	m.set.NewGauge(`fotobank_build_info{version="`+escapeLabel(build.Version)+
		`",commit="`+escapeLabel(build.Commit)+
		`",build_date="`+escapeLabel(build.BuildDate)+`"}`,
		func() float64 { return 1 })

	// Backup pushed gauge + derived seconds-since gauge.
	m.set.NewGauge("fotobank_backup_last_success_unix", func() float64 {
		return float64(m.lastBackupUnix.Load())
	})
	m.set.NewGauge("fotobank_backup_seconds_since_last_success", func() float64 {
		last := m.lastBackupUnix.Load()
		if last == 0 {
			return -1
		}
		return float64(time.Now().Unix() - last)
	})

	// Pull-source gauges. Nil-safe — closures return 0.
	for _, state := range []string{"pending", "working", "failed", "no_preview"} {
		m.set.NewGauge(`fotobank_thumb_queue_depth{state="`+state+`"}`, func() float64 {
			if src.ThumbQueueDepth == nil {
				return 0
			}
			return float64(src.ThumbQueueDepth(state))
		})
	}
	for _, op := range []string{"publish", "revoke"} {
		m.set.NewGauge(`fotobank_share_pending_total{op="`+op+`"}`, func() float64 {
			if src.SharePendingByOp == nil {
				return 0
			}
			return float64(src.SharePendingByOp(op))
		})
	}
	return m
}

// NewTestMetrics builds a Metrics with empty sources and zero build
// info. Tests use this to assert on counter/histogram values without
// needing a fixed registry order or the upstream global.
func NewTestMetrics() *Metrics {
	return NewMetrics(MetricSources{}, BuildInfo{})
}

// SetBackupLastSuccess records the unix-seconds time of the last
// successful backup snapshot. Called from backup.Worker after each
// successful Snapshot. Concurrent-safe.
func (m *Metrics) SetBackupLastSuccess(unix int64) {
	m.lastBackupUnix.Store(unix)
}

// HTTPRequests returns the counter for a (method, route, status_class)
// triple. Counters are auto-created on first call and cached by the
// upstream Set under their full name+labels signature.
func (m *Metrics) HTTPRequests(method, route, statusClass string) *metrics.Counter {
	return m.set.GetOrCreateCounter(`fotobank_http_requests_total{method="` +
		escapeLabel(method) + `",route="` + escapeLabel(route) +
		`",status_class="` + escapeLabel(statusClass) + `"}`)
}

// HTTPRequestDuration returns the explicit-le-bucketed histogram for a
// (method, route) pair.
func (m *Metrics) HTTPRequestDuration(method, route string) *metrics.PrometheusHistogram {
	return m.getOrCreatePrometheusHistogram(
		`fotobank_http_request_duration_seconds`,
		map[string]string{"method": method, "route": route},
		httpDurationBuckets,
	)
}

func (m *Metrics) ThumbJobs(result string) *metrics.Counter {
	return m.set.GetOrCreateCounter(`fotobank_thumb_jobs_total{result="` + escapeLabel(result) + `"}`)
}

func (m *Metrics) ThumbJobDuration(result string) *metrics.PrometheusHistogram {
	return m.getOrCreatePrometheusHistogram(
		`fotobank_thumb_job_duration_seconds`,
		map[string]string{"result": result},
		workerDurationBuckets,
	)
}

func (m *Metrics) ThumbLeasesSwept() *metrics.Counter {
	return m.set.GetOrCreateCounter(`fotobank_thumb_leases_swept_total`)
}

func (m *Metrics) SharePublishes(result string) *metrics.Counter {
	return m.set.GetOrCreateCounter(`fotobank_share_publishes_total{result="` + escapeLabel(result) + `"}`)
}

func (m *Metrics) SharePublishDuration(result string) *metrics.PrometheusHistogram {
	return m.getOrCreatePrometheusHistogram(
		`fotobank_share_publish_duration_seconds`,
		map[string]string{"result": result},
		workerDurationBuckets,
	)
}

func (m *Metrics) ShareRevokes(result string) *metrics.Counter {
	return m.set.GetOrCreateCounter(`fotobank_share_revokes_total{result="` + escapeLabel(result) + `"}`)
}

func (m *Metrics) ShareRevokeDuration(result string) *metrics.PrometheusHistogram {
	return m.getOrCreatePrometheusHistogram(
		`fotobank_share_revoke_duration_seconds`,
		map[string]string{"result": result},
		workerDurationBuckets,
	)
}

func (m *Metrics) BackupSnapshots(result string) *metrics.Counter {
	return m.set.GetOrCreateCounter(`fotobank_backup_snapshots_total{result="` + escapeLabel(result) + `"}`)
}

func (m *Metrics) BackupSnapshotDuration(result string) *metrics.PrometheusHistogram {
	return m.getOrCreatePrometheusHistogram(
		`fotobank_backup_snapshot_duration_seconds`,
		map[string]string{"result": result},
		workerDurationBuckets,
	)
}

func (m *Metrics) BackupRetentionSweeps(result string) *metrics.Counter {
	return m.set.GetOrCreateCounter(`fotobank_backup_retention_sweeps_total{result="` + escapeLabel(result) + `"}`)
}

func (m *Metrics) BackupRetentionDeleted() *metrics.Counter {
	return m.set.GetOrCreateCounter(`fotobank_backup_retention_deleted_total`)
}

// WritePrometheus writes the private set's Prometheus exposition,
// followed by stdlib runtime/process metrics from the upstream helper.
// metrics.WriteProcessMetrics reads runtime state directly and does
// not depend on the upstream global registry.
func (m *Metrics) WritePrometheus(w io.Writer) {
	m.set.WritePrometheus(w)
	metrics.WriteProcessMetrics(w)
}
