package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/dominionthedev/loom/internal/dsl"
	"github.com/dominionthedev/loom/internal/graph"
	"github.com/dominionthedev/loom/internal/workflow"
	"github.com/spf13/cobra"
)

var inspectCmd = &cobra.Command{
	Use:   "inspect <file.lua>",
	Short: "Inspect workflow structure without executing",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		f, err := dsl.Eval(args[0])
		if err != nil {
			return err
		}

		taskFlag, _ := cmd.Flags().GetString("task")

		// Print defined environment.
		if len(f.Agents) > 0 {
			fmt.Println("agents:")
			for name, a := range f.Agents {
				fmt.Printf("  %s (model: %s, think: %s)\n", name, a.Model, a.ThinkLevel)
			}
			fmt.Println()
		}
		if len(f.Scopes) > 0 {
			fmt.Println("scopes:")
			for name, s := range f.Scopes {
				fmt.Printf("  %s — caps: %s\n", name, strings.Join(s.Capabilities, ", "))
			}
			fmt.Println()
		}
		if len(f.Policies) > 0 {
			fmt.Println("policies:")
			for name, p := range f.Policies {
				fmt.Printf("  %s (%s → %s: %s)\n", name, p.Kind, p.Target, strings.Join(p.Match, ", "))
			}
			fmt.Println()
		}

		// Print sequence.
		fmt.Println("sequence:")
		for _, item := range f.Sequence {
			switch item.Kind {
			case workflow.SeqTask:
				if taskFlag != "" && item.Task.Name != taskFlag {
					continue
				}
				printInspectTask(item.Task)

			case workflow.SeqCheckpoint:
				fmt.Printf("  ── checkpoint: %s (%s)", item.Checkpoint.Label, item.Checkpoint.Type)
				if item.Checkpoint.ReviewFile != "" {
					fmt.Printf(" review: %s", item.Checkpoint.ReviewFile)
				}
				fmt.Println()

			case workflow.SeqClear:
				fmt.Println("  ── clear()")
			case workflow.SeqClean:
				fmt.Println("  ── clean()")
			case workflow.SeqFinish:
				fmt.Println("  ── finish()")
			}
		}
		return nil
	},
}

func printInspectTask(task *workflow.Task) {
	g, err := graph.Build(task.Steps)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  task %s: graph error: %v\n", task.Name, err)
		return
	}

	fmt.Printf("\n  task: %s (%d steps, %d level(s))\n", task.Name, len(task.Steps), len(g.Levels))

	for li, level := range g.Levels {
		parallel := len(level) > 1
		if parallel {
			fmt.Printf("    level %d [parallel]\n", li)
		}
		for _, node := range level {
			s := node.Step
			indent := "    "
			if parallel {
				indent = "      "
			}

			switch s.Kind {
			case workflow.StepReason:
				fmt.Printf("%s[reason]  %s", indent, s.Name)
				if s.Prompt != "" {
					fmt.Printf(": %q", truncateStr(s.Prompt, 55))
				}
			case workflow.StepPlan:
				fmt.Printf("%s[plan]    %s", indent, s.Name)
				if s.Prompt != "" {
					fmt.Printf(": %q", truncateStr(s.Prompt, 55))
				}
			default:
				caps := capNames(s.CapCalls)
				fmt.Printf("%s[exec]    %s → %s", indent, s.Name, strings.Join(caps, ", "))
			}

			// Metadata.
			var meta []string
			if len(s.DependsOn) > 0 {
				meta = append(meta, fmt.Sprintf("after: %s", strings.Join(s.DependsOn, ", ")))
			}
			if s.Export {
				meta = append(meta, "export")
			}
			if s.Guard != "" {
				meta = append(meta, fmt.Sprintf("guard: %s", s.Guard))
			}
			if len(meta) > 0 {
				fmt.Printf("  (%s)", strings.Join(meta, " · "))
			}
			fmt.Println()
		}
	}
}

func capNames(calls []workflow.CapCall) []string {
	var names []string
	for _, c := range calls {
		if c.All {
			names = append(names, "all_capabilities")
		} else if c.Name != "" {
			names = append(names, c.Name)
		}
	}
	if len(names) == 0 {
		return []string{"(no caps)"}
	}
	return names
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func init() {
	inspectCmd.Flags().String("task", "", "inspect a specific task")
	rootCmd.AddCommand(inspectCmd)
}
