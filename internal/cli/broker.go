package cli

import (
	"fmt"
	"log/slog"

	"go.kenn.io/fotobank/internal/broker"
	"go.kenn.io/fotobank/internal/brokerexec"
	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/errs"
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
