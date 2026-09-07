package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"go.kenn.io/fotobank/internal/httpapi"
	"go.kenn.io/kit/daemon"
)

// Lifecycle identifies one configured deployment without opening its storage.
type Lifecycle struct {
	DBPath, ConfigPath, Version, Listen string
	StartTimeout, StopTimeout           time.Duration
}

func findDaemon(ctx context.Context, dbPath string) (daemon.RuntimeRecord, daemon.PingInfo, bool, error) {
	store := daemon.RuntimeStore{Dir: dbPath + ".operator"}
	if _, err := os.Stat(store.Dir); errors.Is(err, os.ErrNotExist) {
		return daemon.RuntimeRecord{}, daemon.PingInfo{}, false, nil
	} else if err != nil {
		return daemon.RuntimeRecord{}, daemon.PingInfo{}, false, err
	}
	records, err := store.List()
	if err != nil {
		return daemon.RuntimeRecord{}, daemon.PingInfo{}, false, err
	}
	for _, rec := range records {
		if rec.Service != "fotobank-operator" || !daemon.ProcessAlive(rec.PID) || daemon.CompareRuntimeProcessIdentity(rec) == daemon.ProcessIdentityMismatch {
			continue
		}
		if rec.Network != daemon.NetworkTCP || daemon.RequireLoopback(rec.Address) != nil {
			return rec, daemon.PingInfo{}, false, fmt.Errorf("invalid daemon control endpoint")
		}
		proof, err := daemon.NewProof([]byte(rec.Metadata["token"]))
		if err != nil {
			return rec, daemon.PingInfo{}, false, err
		}
		info, err := proof.Probe(ctx, rec, daemon.ProbeOptions{ExpectedService: "fotobank-operator"})
		if err != nil {
			return rec, info, false, fmt.Errorf("daemon is starting, stopping, or unreachable: %w", err)
		}
		return rec, info, true, nil
	}
	return daemon.RuntimeRecord{}, daemon.PingInfo{}, false, nil
}

func (l Lifecycle) Status(ctx context.Context) (httpapi.DaemonStatus, error) {
	rec, _, found, err := findDaemon(ctx, l.DBPath)
	if err != nil || !found {
		return httpapi.DaemonStatus{}, err
	}
	var out httpapi.DaemonStatus
	err = callRecord(ctx, rec, http.MethodGet, "/api/v1/operator/daemon", nil, &out, "retry daemon status")
	return out, err
}

// Ensure uses Kit's launch lock for starting or replacing a process, shared
// with Stop. Observing an existing daemon uses Kit's lock-free fast path.
// No storage is opened by the client.
func (l Lifecycle) Ensure(ctx context.Context) (httpapi.DaemonStatus, error) {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	store := daemon.RuntimeStore{Dir: l.DBPath + ".operator"}
	var output *os.File
	defer func() {
		if output != nil {
			_ = output.Close()
		}
	}()
	childPID := 0
	manager := daemon.Manager{Store: store}
	manager.FindFunc = func(ctx context.Context) (daemon.RuntimeRecord, daemon.PingInfo, bool, error) {
		rec, info, found, err := findDaemon(ctx, l.DBPath)
		if err == nil && childPID != 0 && !daemon.ProcessAlive(childPID) {
			err = fmt.Errorf("daemon exited before becoming ready")
			// Kit retries discovery errors; a child that has exited is terminal.
			cancel(err)
			return rec, info, false, err
		}
		return rec, info, found && rec.Version == l.Version, err
	}
	manager.Start = func(ctx context.Context) error {
		executable, err := os.Executable()
		if err != nil {
			return err
		}
		if daemon.IsEphemeralExecutable(executable) {
			return fmt.Errorf("build fotobank before starting a background daemon; test and go-run executables cannot own it")
		}
		rec, _, found, err := findDaemon(ctx, l.DBPath)
		if err != nil {
			return err
		}
		if found {
			if err := l.stopRecord(ctx, rec); err != nil {
				return err
			}
		}
		output, err = os.OpenFile(filepath.Join(store.Dir, "daemon.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
		if err != nil {
			return err
		}
		args := []string{"daemon", "run", "--config", l.ConfigPath}
		if l.Listen != "" {
			args = append(args, "--listen", l.Listen)
		}
		return daemon.StartDetached(ctx, daemon.StartDetachedOptions{
			Executable: executable, Args: args,
			Env:    append(os.Environ(), "FOTOBANK_DB_PATH="+l.DBPath),
			Stdout: output, Stderr: output, RefuseEphemeral: true,
			AfterStart: func(cmd *exec.Cmd) { childPID = cmd.Process.Pid },
		})
	}
	_, _, err := manager.Ensure(ctx, l.StartTimeout)
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			err = cause
		}
		if output != nil {
			logPath := filepath.Join(store.Dir, "daemon.log")
			if log, openErr := os.Open(logPath); openErr == nil {
				defer log.Close()
				if stat, statErr := log.Stat(); statErr == nil {
					text, _ := io.ReadAll(io.NewSectionReader(log, max(0, stat.Size()-4096), 4096))
					return httpapi.DaemonStatus{}, fmt.Errorf("%w; daemon log %s:\n%s", err, logPath, text)
				}
			}
		}
		return httpapi.DaemonStatus{}, err
	}
	return l.Status(ctx)
}

func (l Lifecycle) Stop(ctx context.Context) error {
	store := daemon.RuntimeStore{Dir: l.DBPath + ".operator"}
	if _, err := os.Stat(store.Dir); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, l.StopTimeout)
	defer cancel()
	unlock, err := store.AcquireStartLock(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	rec, _, found, err := findDaemon(ctx, l.DBPath)
	if err != nil || !found {
		return err
	}
	return l.stopRecord(ctx, rec)
}

func (l Lifecycle) stopRecord(ctx context.Context, rec daemon.RuntimeRecord) error {
	ctx, cancel := context.WithTimeout(ctx, l.StopTimeout)
	defer cancel()
	// Shutdown is an authenticated HTTP operation on every platform. Do not
	// force-kill a daemon with outstanding writes when the drain budget expires.
	if err := callRecord(ctx, rec, http.MethodPost, "/api/v1/operator/daemon/stop", nil, nil, "inspect daemon status before retrying"); err != nil {
		return err
	}
	store := daemon.RuntimeStore{Dir: l.DBPath + ".operator"}
	recordPath, err := store.Path(rec.PID)
	if err != nil {
		return err
	}
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		_, err := os.Stat(recordPath)
		if errors.Is(err, os.ErrNotExist) || !daemon.ProcessAlive(rec.PID) || daemon.CompareRuntimeProcessIdentity(rec) == daemon.ProcessIdentityMismatch {
			return nil
		}
		if err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("daemon has not finished shutting down: %w", ctx.Err())
		case <-ticker.C:
		}
	}
}
