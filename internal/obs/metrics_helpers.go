package obs

import (
	"sort"
	"strings"

	"github.com/VictoriaMetrics/metrics"
)

// escapeLabel applies Prometheus label-value escaping required by the
// upstream metrics library: backslash and double-quote must be \-escaped;
// newline becomes \n. Defensive escaping; we never expect newlines.
func escapeLabel(s string) string {
	if !strings.ContainsAny(s, `"\`+"\n") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 2)
	for _, r := range s {
		switch r {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// getOrCreatePrometheusHistogram registers (or returns the cached)
// Prometheus-buckets histogram for name+labels with the given le
// buckets. The upstream library keys instruments by the full
// "name{labels}" string, so we build it deterministically (labels
// sorted) and delegate to the set's GetOrCreatePrometheusHistogramExt.
func (m *Metrics) getOrCreatePrometheusHistogram(
	name string,
	labels map[string]string,
	buckets []float64,
) *metrics.PrometheusHistogram {
	full := name
	if len(labels) > 0 {
		full = name + "{" + buildLabelString(labels) + "}"
	}
	return m.set.GetOrCreatePrometheusHistogramExt(full, buckets)
}

func buildLabelString(labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(k)
		b.WriteString(`="`)
		b.WriteString(escapeLabel(labels[k]))
		b.WriteByte('"')
	}
	return b.String()
}
