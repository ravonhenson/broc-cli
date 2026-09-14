package cli

import (
	"fmt"

	"github.com/ravonhenson/drillbit/internal/backend"
	"github.com/ravonhenson/drillbit/internal/backend/restic"
	"github.com/ravonhenson/drillbit/internal/config"
)

func newBackend(cfg *config.Config) (backend.Backend, error) {
	switch cfg.Backend {
	case "restic", "":
		return restic.New(restic.Config{
			Binary:          cfg.Restic.Binary,
			Repository:      cfg.Restic.Repository,
			PasswordCommand: cfg.Restic.PasswordCommand,
			PasswordFile:    cfg.Restic.PasswordFile,
			ExtraArgs:       cfg.Restic.ExtraArgs,
			Env:             cfg.Restic.Env,
		}), nil
	default:
		return nil, fmt.Errorf("unsupported backend %q (only restic is supported currently)", cfg.Backend)
	}
}
