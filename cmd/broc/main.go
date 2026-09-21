// Command broc automates restore-drill verification for backup
// repositories: it restores a rotating sample of real files from real
// snapshots, diffs them against a recorded baseline, and tracks coverage
// over time.
package main

import (
	"os"

	"github.com/ravonhenson/broc-cli/internal/cli"
)

func main() {
	os.Exit(cli.Execute())
}
