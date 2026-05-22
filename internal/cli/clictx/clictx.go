// Package clictx wires the shared dependencies that fotobank CLI
// subcommands need (config, database, owner service) so individual
// subcommand handlers stay small and focused.
package clictx

import (
	"fmt"
	"os"
	"path/filepath"

	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/db"
	"go.kenn.io/fotobank/internal/owners"
	"go.kenn.io/fotobank/internal/service"
)

// LoadOwnerService loads the CLI's configuration, opens the SQLite
// database (honouring FOTOBANK_DB_PATH), and returns an OwnerService
// wired to the resulting read/write pools.
//
// The returned cleanup function closes the database and must be called
// by the caller (typically via defer) when the service is no longer
// needed.
func LoadOwnerService() (*service.OwnerService, func(), error) {
	cfg, err := config.Load(config.DefaultConfigPath())
	if err != nil {
		return nil, nil, fmt.Errorf("load config: %w", err)
	}
	dbPath := os.Getenv("FOTOBANK_DB_PATH")
	if dbPath == "" {
		dbPath = filepath.Join(cfg.Flash.Root, "fotobank.sqlite")
	}
	d, err := db.Open(dbPath)
	if err != nil {
		return nil, nil, fmt.Errorf("open db: %w", err)
	}
	svc := service.NewOwnerService(owners.NewRepo(d.WriteDB(), d.ReadDB()))
	return svc, func() { _ = d.Close() }, nil
}
