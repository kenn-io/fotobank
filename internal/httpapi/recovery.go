package httpapi

import (
	"log/slog"
	"net/http"
	"runtime/debug"
)

// WithRecovery converts handler panics to HTTP 500 responses and logs
// the panic value plus stack trace at ERROR. Sits inside the metrics
// middleware so the recorded status_class is 5xx.
func WithRecovery(logger *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				logger.Error("handler panic",
					"err", rec, "stack", string(debug.Stack()),
					"method", r.Method, "path", r.URL.Path)
				http.Error(w, http.StatusText(http.StatusInternalServerError),
					http.StatusInternalServerError)
			}()
			next.ServeHTTP(w, r)
		})
	}
}
