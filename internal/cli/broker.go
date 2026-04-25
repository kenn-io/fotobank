package cli

import (
	"fmt"
	"log/slog"

	"github.com/wesm/fotobank/internal/broker"
	"github.com/wesm/fotobank/internal/brokerexec"
	"github.com/wesm/fotobank/internal/config"
	"github.com/wesm/fotobank/internal/errs"
)

// newBrokerClient returns the broker.BrokerClient implementation
// matching cfg.Mode. Used by runServer at boot. config.Validate
// already rejects unknown modes; the default arm here is
// defence-in-depth.
func newBrokerClient(cfg config.Broker, logger *slog.Logger) (broker.BrokerClient, error) {
	switch cfg.Mode {
	case "stub":
		return broker.NoopBroker{}, nil
	case "exec":
		return brokerexec.New(brokerexec.Config{
			Command:          cfg.Exec.Command,
			PublishScopeArgs: cfg.Exec.PublishScopeArgs,
			RevokeScopeArgs:  cfg.Exec.RevokeScopeArgs,
			CallTimeout:      cfg.Exec.CallTimeout,
			Env:              cfg.Exec.Env,
			Logger:           logger,
		})
	default:
		return nil, fmt.Errorf("%w: unknown broker mode %q", errs.ErrBadConfiguration, cfg.Mode)
	}
}
