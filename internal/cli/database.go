package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/gofrs/flock"

	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/errs"
)

// databaseLifetime is the daemon's shared process-lifetime fence for the
// metadata database, separate from short application mutation locks.
type databaseLifetime struct {
	path      string
	lock      *flock.Flock
	closeOnce sync.Once
	closeErr  error
}

func acquireDatabaseLifetime(path string) (*databaseLifetime, error) {
	canonical, err := config.ResolveDatabasePath(path)
	if err != nil {
		return nil, err
	}
	lockPath := lockPathFor(canonical)
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, fmt.Errorf("create database lock directory: %w", err)
	}
	lock := flock.New(lockPath)
	locked, err := lock.TryRLock()
	if err != nil {
		return nil, fmt.Errorf("acquire database lifetime lock: %w", err)
	}
	if !locked {
		return nil, fmt.Errorf(
			"open database: %w: another process is replacing the database at %s",
			errs.ErrAlreadyExists, canonical)
	}
	return &databaseLifetime{path: canonical, lock: lock}, nil
}

func (l *databaseLifetime) Close() error {
	if l == nil {
		return nil
	}
	l.closeOnce.Do(func() {
		l.closeErr = l.lock.Unlock()
	})
	return l.closeErr
}

type databaseHandle struct {
	*db.DB
	lifetime  *databaseLifetime
	closeOnce sync.Once
	closeErr  error
}

func openDatabasePath(path string) (*databaseHandle, error) {
	lifetime, err := acquireDatabaseLifetime(path)
	if err != nil {
		return nil, err
	}
	database, err := db.Open(lifetime.path)
	if err != nil {
		return nil, errors.Join(err, lifetime.Close())
	}
	return &databaseHandle{DB: database, lifetime: lifetime}, nil
}

func (d *databaseHandle) Path() string {
	if d == nil || d.lifetime == nil {
		return ""
	}
	return d.lifetime.path
}

func (d *databaseHandle) Close() error {
	if d == nil {
		return nil
	}
	d.closeOnce.Do(func() {
		d.closeErr = errors.Join(d.DB.Close(), d.lifetime.Close())
	})
	return d.closeErr
}
