package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ravonhenson/drillbit/internal/config"
	"github.com/ravonhenson/drillbit/internal/notify"
	"github.com/ravonhenson/drillbit/internal/report"
	"github.com/ravonhenson/drillbit/internal/state"
	"github.com/ravonhenson/drillbit/internal/verify"
)

var runCmd = &cobra.Command{
	Use:   "run",
	Short: "Restore and verify one rotating sample of files from the repository",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(cfgPath)
		if err != nil {
			return err
		}

		be, err := newBackend(cfg)
		if err != nil {
			return err
		}

		store, err := state.Open(cfg.State.Path)
		if err != nil {
			return err
		}
		defer store.Close()

		ctx := cmd.Context()
		notify.StartPing(ctx, cfg.Notify)

		result, runErr := verify.Run(ctx, cfg, be, store)

		if notifyErr := notify.Send(ctx, cfg.Notify, result, runErr); notifyErr != nil {
			fmt.Fprintln(cmd.ErrOrStderr(), "drillbit: notification error:", notifyErr)
		}

		if jsonOut {
			_ = report.PrintJSON(cmd.OutOrStdout(), result, runErr)
		} else {
			report.PrintText(cmd.OutOrStdout(), result, runErr)
		}

		exitCode = report.ExitCode(result, runErr)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(runCmd)
}
