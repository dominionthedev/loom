package cmd

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/log"
	"github.com/dominionthedev/loom/internal/capability"
	"github.com/dominionthedev/loom/internal/dsl"
	"github.com/dominionthedev/loom/internal/model"
	"github.com/dominionthedev/loom/internal/orchestrator"
	"github.com/dominionthedev/loom/internal/storage"
	"github.com/dominionthedev/loom/internal/workflow"
	"github.com/spf13/cobra"
)

var runCmd = &cobra.Command{
	Use:   "run <file.lua>",
	Short: "Load and execute a workflow file",
	Long: `Evaluate a Loom workflow file and run its tasks.

  loom run fix.lua
  loom run fix.lua --task verify
  loom run fix.lua --dry-run
  loom run fix.lua -v              # verbose: show tool calls + model replies`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		filePath := args[0]
		targetTask, _ := cmd.Flags().GetString("task")
		dryRun, _ := cmd.Flags().GetBool("dry-run")

		// ── Load DSL ─────────────────────────────────────────────────
		f, err := dsl.Eval(filePath)
		if err != nil {
			return err
		}
		if len(f.Sequence) == 0 {
			return fmt.Errorf("no tasks found in %s", filePath)
		}

		if dryRun {
			return runDryRun(f, targetTask)
		}

		// ── Runtime setup ─────────────────────────────────────────────
		workspace := flagWorkspace
		if workspace == "" {
			workspace, _ = os.Getwd()
		}

		store, err := storage.New(workspace)
		if err != nil {
			return fmt.Errorf("storage: %w", err)
		}

		caps := capability.New()

		modelCfg := model.Config{
			Default: flagModel,
			Medium:  flagModelMid,
			High:    flagModelHigh,
		}
		router := model.NewRouter(modelCfg)

		logger := log.New(os.Stderr)
		if flagVerbose {
			logger.SetLevel(log.DebugLevel)
		}

		orch := orchestrator.New(caps, router, store, logger, flagVerbose)

		// ── Execute ───────────────────────────────────────────────────
		fmt.Fprintln(os.Stderr)
		result := orch.Run(context.Background(), f, targetTask)

		// ── Print results ─────────────────────────────────────────────
		fmt.Println()
		for _, tr := range result.Tasks {
			fmt.Printf("  task: %s\n", tr.TaskName)
			for _, sr := range tr.Steps {
				printStepResult(sr)
			}
		}
		fmt.Println()

		// ── Denied capabilities — non-blocking report ──────────────────
		printDeniedReport(result.Tasks)

		if result.Error != nil {
			fmt.Fprintf(os.Stderr, "  failed: %v\n", result.Error)
			os.Exit(1)
		}

		fmt.Fprintln(os.Stderr, "  done.")
		return nil
	},
}

// printDeniedReport surfaces capabilities the agent needed but didn't have
// access to. Non-blocking — just flags a possible missing declaration.
func printDeniedReport(tasks []*workflow.TaskResult) {
	var any bool
	for _, tr := range tasks {
		if len(tr.Denied) > 0 {
			any = true
			break
		}
	}
	if !any {
		return
	}

	fmt.Println("  ⚠ capabilities the agent needed but didn't have access to:")
	for _, tr := range tasks {
		for _, d := range tr.Denied {
			fmt.Printf("      task=%s step=%s tool=%s — %s\n", tr.TaskName, d.Step, d.Tool, d.Reason)
		}
	}
	fmt.Println("    add these to scope/step capabilities if the agent should have access.")
	fmt.Println()
}

func printStepResult(sr *workflow.StepResult) {
	switch sr.Status {
	case workflow.StepOK:
		fmt.Printf("    ✓  %s\n", sr.StepName)
		if sr.Output != "" {
			lines := strings.Split(strings.TrimRight(sr.Output, "\n"), "\n")
			preview := lines
			truncated := false
			if len(lines) > 6 {
				preview = lines[:6]
				truncated = true
			}
			for _, line := range preview {
				fmt.Printf("       %s\n", line)
			}
			if truncated {
				fmt.Printf("       … (%d more lines)\n", len(lines)-6)
			}
		}
	case workflow.StepFailed:
		fmt.Printf("    ✗  %s\n", sr.StepName)
		if sr.Error != nil {
			for _, line := range strings.Split(sr.Error.Error(), "\n") {
				if strings.TrimSpace(line) != "" {
					fmt.Printf("       %s\n", line)
				}
			}
		}
	case workflow.StepBlocked:
		fmt.Printf("    ⊘  %s (guard blocked)\n", sr.StepName)
		if sr.Error != nil {
			fmt.Printf("       %s\n", sr.Error.Error())
		}
	case workflow.StepSkipped:
		fmt.Printf("    –  %s\n", sr.StepName)
	}
}

func runDryRun(f *workflow.File, targetTask string) error {
	fmt.Println("dry run — no execution")
	fmt.Println()
	for _, item := range f.Sequence {
		switch item.Kind {
		case workflow.SeqTask:
			if targetTask != "" && item.Task.Name != targetTask {
				continue
			}
			fmt.Printf("  task: %s (%d steps)\n", item.Task.Name, len(item.Task.Steps))
			for _, s := range item.Task.Steps {
				deps := ""
				if len(s.DependsOn) > 0 {
					deps = fmt.Sprintf(" → after: %s", strings.Join(s.DependsOn, ", "))
				}
				fmt.Printf("    %s [%s]%s\n", s.Name, s.Kind, deps)
			}
		case workflow.SeqCheckpoint:
			fmt.Printf("  checkpoint: %s (%s)\n", item.Checkpoint.Label, item.Checkpoint.Type)
		case workflow.SeqClear:
			fmt.Println("  clear()")
		case workflow.SeqClean:
			fmt.Println("  clean()")
		case workflow.SeqFinish:
			fmt.Println("  finish()")
		}
	}
	return nil
}

func init() {
	runCmd.Flags().String("task", "", "run a specific task by name")
	runCmd.Flags().Bool("dry-run", false, "show execution plan without running")
	rootCmd.AddCommand(runCmd)
}
