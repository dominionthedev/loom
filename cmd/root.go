package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Version is set at build time via ldflags:
// -X github.com/dominionthedev/loom/cmd.Version=v0.1
var Version = "dev"

var (
	flagModel     string
	flagWorkspace string
)

var rootCmd = &cobra.Command{
	Use:     "loom",
	Short:   "A programmable AI workflow runtime for development operations",
	Version: Version,
	Long: `Loom is a programmable AI workflow runtime.
You define the structure. The runtime executes it. The agent thinks inside it.

  loom run fix.lua               # run all tasks in a workflow file
  loom run fix.lua --task verify # run a specific task
  loom inspect fix.lua           # inspect task/step structure`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVar(&flagModel, "model", "llama3.2", "default LLM model name")
	rootCmd.PersistentFlags().StringVar(&flagWorkspace, "workspace", "", "workspace root (default: cwd)")
}
