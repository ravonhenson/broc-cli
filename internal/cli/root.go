// Package cli wires up broc's cobra commands.
package cli

import (
	"github.com/spf13/cobra"

	"github.com/ravonhenson/broc-cli/internal/report"
)

// version is set at build time via -ldflags "-X .../cli.version=...".
var version = "dev"

var (
	cfgPath string
	jsonOut bool
)

// exitCode is set by whichever subcommand ran; Execute returns it once
// cobra is done. Kept separate from cobra's own error return so that
// "the drill ran but found a mismatch" (exit 1) and "the drill couldn't
// run at all" (exit 2) stay distinguishable.
var exitCode = report.ExitOK

var rootCmd = &cobra.Command{
	Use:   "broc",
	Short: "Automated restore-drill verification for backup repositories",
	Long: `broc periodically restores a rotating sample of real files from real
snapshots in your backup repository, diffs them against a recorded baseline,
and tracks coverage over time - so "the backup job succeeded" and "you can
actually restore your data" stop being different claims.

Supports restic today; borg and kopia backends are on the roadmap.`,
	SilenceUsage: true,
	Version:      version,
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&cfgPath, "config", "c", "broc.yaml", "path to broc config file")
	rootCmd.PersistentFlags().BoolVar(&jsonOut, "json", false, "emit structured JSON output")
}

// Execute runs the CLI and returns the process exit code to use.
func Execute() int {
	if err := rootCmd.Execute(); err != nil {
		return report.ExitError
	}
	return exitCode
}
