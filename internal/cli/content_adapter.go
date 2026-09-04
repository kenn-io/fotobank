package cli

import (
	"go.kenn.io/fotobank/internal/config"
	"go.kenn.io/fotobank/internal/content"
)

func contentAdapterConfig(cfg *config.Config, allowUnavailableNAS bool) content.Config {
	return content.Config{
		Root: cfg.Docbank.Root,
		ManagedRoots: []content.ManagedRoot{
			{
				Path:             cfg.ConfiguredNASRoot(),
				ValidatedPath:    cfg.NAS.Root,
				AllowUnavailable: allowUnavailableNAS,
			},
			{
				Path:            cfg.ConfiguredFlashRoot(),
				ValidatedPath:   cfg.Flash.Root,
				CreateIfMissing: true,
			},
		},
	}
}
