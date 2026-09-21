package cli

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/ravonhenson/broc-cli/internal/config"
	"github.com/ravonhenson/broc-cli/internal/report"
	"github.com/ravonhenson/broc-cli/internal/state"
)

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show verification coverage and recent run history",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load(cfgPath)
		if err != nil {
			return err
		}

		store, err := state.Open(cfg.State.Path)
		if err != nil {
			return err
		}
		defer store.Close()

		records, err := store.All()
		if err != nil {
			return err
		}
		runs, err := store.RecentRuns(5)
		if err != nil {
			return err
		}

		var ok, mismatch, errCount int
		for _, r := range records {
			switch r.LastStatus {
			case state.StatusMismatch:
				mismatch++
			case state.StatusError:
				errCount++
			default:
				ok++
			}
		}

		if jsonOut {
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			if err := enc.Encode(map[string]any{
				"repo":                 cfg.Name,
				"total_verified_pairs": len(records),
				"ok":                   ok,
				"mismatches":           mismatch,
				"errors":               errCount,
				"recent_runs":          runs,
			}); err != nil {
				return err
			}
		} else {
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "repo: %s\n", cfg.Name)
			fmt.Fprintf(out, "coverage: %d (snapshot,file) pairs ever verified - %d ok, %d mismatches, %d errors\n",
				len(records), ok, mismatch, errCount)
			if mismatch > 0 {
				fmt.Fprintf(out, "\n! %d file(s) currently show a content mismatch against their recorded baseline - investigate before trusting restores from this repo.\n", mismatch)
			}
			fmt.Fprintln(out, "\nrecent runs:")
			if len(runs) == 0 {
				fmt.Fprintln(out, "  (none yet - run `broc run`)")
			}
			for _, r := range runs {
				status := "ok"
				if !r.Ok {
					status = "FAILED"
				}
				fmt.Fprintf(out, "  %s  %-6s  checked=%d new=%d mismatches=%d errors=%d\n",
					r.StartedAt.Format(time.RFC3339), status, r.FilesChecked, r.NewBaselines, r.Mismatches, r.Errors)
			}
		}

		if mismatch > 0 || errCount > 0 {
			exitCode = report.ExitFindings
		}
		return nil
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
