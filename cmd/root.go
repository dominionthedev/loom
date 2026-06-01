package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Version is set at build time via ldflags.
var Version = "dev"

var (
	flagModel     string
	flagModelMid  string
	flagModelHigh string
	flagWorkspace string
	flagVerbose   bool
)

var rootCmd = &cobra.Command{
	Use:     "loom",
	Short:   "A programmable AI workflow runtime for development operations",
	Version: Version,
	Long: `Loom is a programmable AI workflow runtime.
You define the structure. The runtime executes it. The agent thinks inside it.

  loom run fix.lua               # run all tasks
  loom run fix.lua --task verify # run one task
  loom inspect fix.lua           # inspect structure
  loom models                    # list available models`,
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
	rootCmd.PersistentFlags().StringVar(&flagModel, "model", "gemma3:4b",
		"default model (think: low or unset)")
	rootCmd.PersistentFlags().StringVar(&flagModelMid, "model-mid", "devstral-small-2:24b",
		"model for think(\"medium\")")
	rootCmd.PersistentFlags().StringVar(&flagModelHigh, "model-high", "devstral-small-2:24b",
		"model for think(\"high\") and plan()")
	rootCmd.PersistentFlags().StringVar(&flagWorkspace, "workspace", "",
		"workspace root (default: cwd)")
	rootCmd.PersistentFlags().BoolVarP(&flagVerbose, "verbose", "v", false,
		"verbose output — show tool calls, model replies, capability results")
}
