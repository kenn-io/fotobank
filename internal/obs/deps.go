// Package obs is the fotobank observability layer. The blank import
// here pins github.com/VictoriaMetrics/metrics into go.mod so go mod
// tidy does not strip the dep before Task 3 / Task 4 add real
// importers (logger.go and metrics.go). Delete this file once Task 4
// lands; the real obs/metrics.go will own the import.
package obs

import _ "github.com/VictoriaMetrics/metrics"
