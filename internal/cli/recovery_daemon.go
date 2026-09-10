package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/gofrs/flock"
	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/fotobank/internal/operator"
	"go.kenn.io/fotobank/internal/service"
	"go.kenn.io/fotobank/internal/version"
)

// Both modes reserve the same deployment before opening storage or listeners.
// The config file, unlike the catalog, must remain available during recovery.
func runServer(ctx context.Context, opts serverOpts) error {
	var removeRuntime func()
	defer func() {
		if removeRuntime != nil {
			removeRuntime()
		}
	}()
	lifecycle, err := daemonLifecycle(opts.cfgPath, opts.listen)
	if err != nil {
		return err
	}
	opts.cfgPath = lifecycle.ConfigPath
	lock := flock.New(opts.cfgPath + ".daemon.lock")
	locked, err := lock.TryLock()
	if err != nil {
		return fmt.Errorf("lock daemon deployment: %w", err)
	}
	if !locked {
		return fmt.Errorf("another Fotobank daemon is using this configuration; stop it before switching modes")
	}
	defer func() { _ = lock.Unlock() }()
	if !opts.recovery {
		return runPhotoServer(ctx, opts, &removeRuntime)
	}
	cfg, err := config.LoadUnchecked(opts.cfgPath)
	if err != nil {
		return err
	}
	if err := cfg.Daemon.Validate(); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	op, err := operator.Start(ctx, opts.cfgPath, version.Short, cfg.Daemon.ListenAddress, "", true, stop,
		httpapi.Deps{BackupRepository: &service.BackupRepositoryService{}})
	if err != nil {
		return err
	}
	removeRuntime = op.RemoveRecord
	defer op.Close()
	select {
	case <-ctx.Done():
		return nil
	case err := <-op.Fatal:
		return err
	}
}
