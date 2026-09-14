package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ravonhenson/drillbit/internal/config"
)

var (
	initName            string
	initBackend         string
	initRepository      string
	initPasswordCommand string
	initOutput          string
	initForce           bool
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Create a drillbit config file for a backup repository",
	RunE: func(cmd *cobra.Command, args []string) error {
		if initName == "" {
			return fmt.Errorf("--name is required")
		}
		out := initOutput
		if out == "" {
			out = cfgPath
		}
		if !initForce {
			if _, err := os.Stat(out); err == nil {
				return fmt.Errorf("%s already exists (use --force to overwrite)", out)
			}
		}

		cfg := &config.Config{
			Name:    initName,
			Backend: initBackend,
			Restic: config.ResticConfig{
				Repository:      initRepository,
				PasswordCommand: initPasswordCommand,
			},
			Sample: config.SampleConfig{
				FilesPerRun:       15,
				ReverifyAfterDays: 30,
			},
		}
		cfg.ApplyDefaults()

		if err := cfg.Save(out); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n\nnext: drillbit run --config %s\n", out, out)
		return nil
	},
}

func init() {
	initCmd.Flags().StringVar(&initName, "name", "", "repository name (required; derives default state/scratch paths)")
	initCmd.Flags().StringVar(&initBackend, "backend", "restic", "backend type")
	initCmd.Flags().StringVar(&initRepository, "repository", "", "restic repository location (optional; falls back to $RESTIC_REPOSITORY)")
	initCmd.Flags().StringVar(&initPasswordCommand, "password-command", "", "command restic runs for the repository password (optional; falls back to $RESTIC_PASSWORD_COMMAND)")
	initCmd.Flags().StringVar(&initOutput, "output", "", "path to write the config to (default: --config value)")
	initCmd.Flags().BoolVar(&initForce, "force", false, "overwrite an existing config file")
	rootCmd.AddCommand(initCmd)
}
