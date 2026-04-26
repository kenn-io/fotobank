package obs

import (
	"net/http"
	"net/http/pprof"
)

// AdminConfig assembles the admin listener.
type AdminConfig struct {
	Metrics      *Metrics
	Ready        *Ready
	Checks       []ReadyCheck
	ReadyzCfg    ReadyzConfig
	PprofEnabled bool
}

// NewAdminMux returns the http.Handler that mounts /metrics, /readyz,
// and (when enabled) /debug/pprof/*. No identity middleware; the
// listener it serves on must be loopback or unix-bound.
func NewAdminMux(cfg AdminConfig) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		cfg.Metrics.WritePrometheus(w)
	})
	mux.Handle("/readyz", NewReadyzHandler(cfg.Ready, cfg.Checks, cfg.ReadyzCfg))

	if cfg.PprofEnabled {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}
	return mux
}
