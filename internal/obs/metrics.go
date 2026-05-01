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
	AIJobsDepth      func(task, status string) int64
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

	// AI gateway/global state pushed by the AI worker. Stored atomically
	// so the scrape closure can read consistently. 0 or 1.
	ackRequired     atomic.Int64
	visionReachable atomic.Int64
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
	for _, task := range []string{"tag", "caption"} {
		for _, status := range []string{"pending", "working", "blocked"} {
			t, s := task, status // capture for closure
			m.set.NewGauge(`fotobank_ai_jobs_depth{task="`+t+`",status="`+s+`"}`, func() float64 {
				if src.AIJobsDepth == nil {
					return 0
				}
				return float64(src.AIJobsDepth(t, s))
			})
		}
	}
	m.set.NewGauge(`fotobank_ai_acknowledgement_required`, func() float64 {
		return float64(m.ackRequired.Load())
	})
	m.set.NewGauge(`fotobank_ai_endpoint_reachable{kind="vision"}`, func() float64 {
		return float64(m.visionReachable.Load())
	})
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
//
// CARDINALITY CONTRACT: route MUST be the registered ServeMux pattern
// (e.g. "/api/v1/media/{id}"), NOT the raw r.URL.Path. Passing raw
// paths is a cardinality bomb — every photo ID, album ID, and
// path-traversal probe becomes a permanent series. T9's middleware
// (httpapi/middleware.go) is responsible for normalizing routes via
// r.Pattern + {x}→:x rewrite before calling this accessor.
func (m *Metrics) HTTPRequests(method, route, statusClass string) *metrics.Counter {
	return m.set.GetOrCreateCounter(`fotobank_http_requests_total{method="` +
		escapeLabel(method) + `",route="` + escapeLabel(route) +
		`",status_class="` + escapeLabel(statusClass) + `"}`)
}

// HTTPRequestDuration returns the explicit-le-bucketed histogram for a
// (method, route) pair. See HTTPRequests for the route-template
// cardinality contract — same rule applies here.
func (m *Metrics) HTTPRequestDuration(method, route string) *metrics.PrometheusHistogram {
	return m.getOrCreatePrometheusHistogram(
		`fotobank_http_request_duration_seconds`,
		map[string]string{"method": method, "route": route},
		httpDurationBuckets,
	)
}

// ThumbJobs returns the result counter for the thumbnail worker.
// result ∈ {"ok", "failed", "no_preview"}.
func (m *Metrics) ThumbJobs(result string) *metrics.Counter {
	return m.set.GetOrCreateCounter(`fotobank_thumb_jobs_total{result="` + escapeLabel(result) + `"}`)
}

// ThumbJobDuration returns the result histogram for the thumbnail worker.
// result ∈ {"ok", "failed", "no_preview"}; uses workerDurationBuckets.
func (m *Metrics) ThumbJobDuration(result string) *metrics.PrometheusHistogram {
	return m.getOrCreatePrometheusHistogram(
		`fotobank_thumb_job_duration_seconds`,
		map[string]string{"result": result},
		workerDurationBuckets,
	)
}

// ThumbLeasesSwept counts stale-lease rows reclaimed by the thumb
// worker's periodic SweepLeases call.
func (m *Metrics) ThumbLeasesSwept() *metrics.Counter {
	return m.set.GetOrCreateCounter(`fotobank_thumb_leases_swept_total`)
}

// SharePublishes counts share-broker publish results.
// result ∈ {"ok", "retry", "terminal_fail"}.
func (m *Metrics) SharePublishes(result string) *metrics.Counter {
	return m.set.GetOrCreateCounter(`fotobank_share_publishes_total{result="` + escapeLabel(result) + `"}`)
}

// SharePublishDuration is the publish-result histogram.
// result ∈ {"ok", "retry", "terminal_fail"}; uses workerDurationBuckets.
func (m *Metrics) SharePublishDuration(result string) *metrics.PrometheusHistogram {
	return m.getOrCreatePrometheusHistogram(
		`fotobank_share_publish_duration_seconds`,
		map[string]string{"result": result},
		workerDurationBuckets,
	)
}

// ShareRevokes counts share-broker revoke results.
// result ∈ {"ok", "retry", "terminal_fail"}.
func (m *Metrics) ShareRevokes(result string) *metrics.Counter {
	return m.set.GetOrCreateCounter(`fotobank_share_revokes_total{result="` + escapeLabel(result) + `"}`)
}

// ShareRevokeDuration is the revoke-result histogram.
// result ∈ {"ok", "retry", "terminal_fail"}; uses workerDurationBuckets.
func (m *Metrics) ShareRevokeDuration(result string) *metrics.PrometheusHistogram {
	return m.getOrCreatePrometheusHistogram(
		`fotobank_share_revoke_duration_seconds`,
		map[string]string{"result": result},
		workerDurationBuckets,
	)
}

// BackupSnapshots counts per-tick backup snapshot outcomes.
// result ∈ {"ok", "failed"}.
func (m *Metrics) BackupSnapshots(result string) *metrics.Counter {
	return m.set.GetOrCreateCounter(`fotobank_backup_snapshots_total{result="` + escapeLabel(result) + `"}`)
}

// BackupSnapshotDuration is the snapshot-outcome histogram.
// result ∈ {"ok", "failed"}; uses workerDurationBuckets.
func (m *Metrics) BackupSnapshotDuration(result string) *metrics.PrometheusHistogram {
	return m.getOrCreatePrometheusHistogram(
		`fotobank_backup_snapshot_duration_seconds`,
		map[string]string{"result": result},
		workerDurationBuckets,
	)
}

// BackupRetentionSweeps counts the per-tick retention sweep outcomes.
// result ∈ {"ok", "failed"}.
func (m *Metrics) BackupRetentionSweeps(result string) *metrics.Counter {
	return m.set.GetOrCreateCounter(`fotobank_backup_retention_sweeps_total{result="` + escapeLabel(result) + `"}`)
}

// BackupRetentionDeleted counts files deleted by the retention sweep.
// Incremented by the per-tick Sweep result count.
func (m *Metrics) BackupRetentionDeleted() *metrics.Counter {
	return m.set.GetOrCreateCounter(`fotobank_backup_retention_deleted_total`)
}

// AIJobs returns the (task, result) completion counter.
// result ∈ {"ok", "failed", "skipped"}; task ∈ {"tag", "caption"}.
func (m *Metrics) AIJobs(task, result string) *metrics.Counter {
	return m.set.GetOrCreateCounter(`fotobank_ai_jobs_completed_total{task="` +
		escapeLabel(task) + `",result="` + escapeLabel(result) + `"}`)
}

// AIRequestDuration is the (task, outcome) gateway-request histogram.
// outcome ∈ {"ok", "transient", "provider_4xx", "malformed"}; uses workerDurationBuckets.
func (m *Metrics) AIRequestDuration(task, outcome string) *metrics.PrometheusHistogram {
	return m.getOrCreatePrometheusHistogram(
		`fotobank_ai_request_duration_seconds`,
		map[string]string{"task": task, "outcome": outcome},
		workerDurationBuckets,
	)
}

// SetAIVisionReachable flips the fotobank_ai_endpoint_reachable{kind="vision"} gauge.
func (m *Metrics) SetAIVisionReachable(reachable bool) {
	if reachable {
		m.visionReachable.Store(1)
	} else {
		m.visionReachable.Store(0)
	}
}

// SetAIAcknowledgementRequired flips fotobank_ai_acknowledgement_required.
func (m *Metrics) SetAIAcknowledgementRequired(required bool) {
	if required {
		m.ackRequired.Store(1)
	} else {
		m.ackRequired.Store(0)
	}
}

// WritePrometheus writes the private set's Prometheus exposition,
// followed by stdlib runtime/process metrics from the upstream helper.
// metrics.WriteProcessMetrics reads runtime state directly and does
// not depend on the upstream global registry.
//
// Safe under concurrent scrapes per upstream metrics v1.43.2; revisit
// the assumption on upgrade. Concurrent scrape × backup-write race:
// fotobank_backup_last_success_unix and fotobank_backup_seconds_since_last_success
// each call lastBackupUnix.Load() in independent gauge closures, so a
// scrape that overlaps with SetBackupLastSuccess can emit a torn pair
// where seconds_since is slightly under-estimated. The deviation is
// bounded by a single backup write and is harmless to consumers.
func (m *Metrics) WritePrometheus(w io.Writer) {
	m.set.WritePrometheus(w)
	metrics.WriteProcessMetrics(w)
}
