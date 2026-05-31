// Package orchestrator executes Loom workflow files.
// It drives the sequence: tasks, checkpoints, clear, clean, finish.
// The agent runs each step — the orchestrator does NOT dispatch capabilities.
// It provides tools to the agent, then executes what the agent calls.
package orchestrator

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/log"
	"github.com/dominionthedev/loom/internal/agent"
	"github.com/dominionthedev/loom/internal/capability"
	"github.com/dominionthedev/loom/internal/graph"
	"github.com/dominionthedev/loom/internal/model"
	"github.com/dominionthedev/loom/internal/storage"
	"github.com/dominionthedev/loom/internal/workflow"
)

// Orchestrator executes a workflow File.
type Orchestrator struct {
	caps   *capability.Registry
	router *model.Router
	store  *storage.Store
	log    *log.Logger
}

// New creates an Orchestrator.
func New(caps *capability.Registry, router *model.Router, store *storage.Store, logger *log.Logger) *Orchestrator {
	return &Orchestrator{caps: caps, router: router, store: store, log: logger}
}

// Run executes a workflow file's sequence.
// targetTask limits execution to one named task (empty = run all).
func (o *Orchestrator) Run(ctx context.Context, f *workflow.File, targetTask string) *workflow.RunResult {
	runID := fmt.Sprintf("%d", time.Now().UnixNano())
	start := time.Now()
	result := &workflow.RunResult{}

	o.log.Info("loom run started", "tasks", countTasks(f.Sequence))

	for _, item := range f.Sequence {
		if ctx.Err() != nil {
			break
		}

		switch item.Kind {
		case workflow.SeqTask:
			if targetTask != "" && item.Task.Name != targetTask {
				continue
			}
			tr := o.runTask(ctx, f, item.Task)
			result.Tasks = append(result.Tasks, tr)
			if tr.Error != nil {
				result.Error = tr.Error
				o.log.Error("task failed", "task", item.Task.Name, "err", tr.Error)
				goto done
			}

		case workflow.SeqCheckpoint:
			if err := o.runCheckpoint(item.Checkpoint); err != nil {
				result.Error = err
				goto done
			}

		case workflow.SeqClear:
			o.log.Info("clear")

		case workflow.SeqClean:
			o.log.Info("clean")

		case workflow.SeqFinish:
			o.log.Info("finish — workflow complete")
		}
	}

done:
	o.store.RecordRun(storage.RunRecord{
		RunID:   runID,
		Success: result.Error == nil,
		Tasks:   len(result.Tasks),
		Error: func() string {
			if result.Error != nil {
				return result.Error.Error()
			}
			return ""
		}(),
		StartedAt:  start,
		FinishedAt: time.Now(),
	})

	return result
}

// runTask executes one task.
func (o *Orchestrator) runTask(ctx context.Context, f *workflow.File, task *workflow.Task) *workflow.TaskResult {
	result := &workflow.TaskResult{TaskName: task.Name}

	use := task.Use
	if use == nil {
		use = f.Use
	}
	if use == nil {
		use = &workflow.UseConfig{}
	}
	if use.Scope == nil {
		use.Scope = &workflow.Scope{Name: "default"}
	}

	o.log.Info("task", "name", task.Name, "steps", len(task.Steps))

	g, err := graph.Build(task.Steps)
	if err != nil {
		result.Error = fmt.Errorf("task %s: %w", task.Name, err)
		return result
	}

	// One agent per task, locked to its use() config.
	ag := agent.New(use, o.caps, o.store, o.router)

	// Accumulated context flows through steps.
	var ctxMu sync.Mutex
	var stepContext strings.Builder

	for levelIdx, level := range g.Levels {
		if ctx.Err() != nil {
			break
		}

		levelResults := o.execLevel(ctx, use, level, ag, func() string {
			ctxMu.Lock()
			defer ctxMu.Unlock()
			return stepContext.String()
		})

		fatal := false
		for _, sr := range levelResults {
			result.Steps = append(result.Steps, sr)

			if sr.Output != "" {
				ctxMu.Lock()
				fmt.Fprintf(&stepContext, "%s\n", sr.Output)
				ctxMu.Unlock()
			}

			if sr.Status == workflow.StepFailed && sr.Error != nil {
				step := findStep(task.Steps, sr.StepName)
				if step == nil || step.OnFailure == workflow.OnFailureStop || step.OnFailure == "" {
					result.Error = sr.Error
					fatal = true
				}
			}
		}

		if fatal {
			o.log.Debug("stopped at level", "level", levelIdx)
			break
		}
	}

	return result
}

// execLevel runs all steps at one graph level, in parallel if more than one.
func (o *Orchestrator) execLevel(
	ctx context.Context,
	use *workflow.UseConfig,
	level []*graph.Node,
	ag *agent.Agent,
	getCtx func() string,
) []*workflow.StepResult {

	if len(level) == 1 {
		return []*workflow.StepResult{o.execStep(ctx, use, level[0].Step, ag, getCtx())}
	}

	results := make([]*workflow.StepResult, len(level))
	var wg sync.WaitGroup
	for i, node := range level {
		wg.Add(1)
		go func(idx int, step *workflow.Step) {
			defer wg.Done()
			results[idx] = o.execStep(ctx, use, step, ag, getCtx())
		}(i, node.Step)
	}
	wg.Wait()
	return results
}

// execStep hands a step to the agent and returns the result.
// The agent drives everything — tool selection, execution order, final output.
func (o *Orchestrator) execStep(
	ctx context.Context,
	use *workflow.UseConfig,
	step *workflow.Step,
	ag *agent.Agent,
	accumulatedContext string,
) *workflow.StepResult {

	o.log.Info("step", "name", step.Name, "kind", step.Kind)

	// Import exported context from depends_on.
	importedCtx := accumulatedContext
	for _, depName := range step.DependsOn {
		if imported := ag.ImportFrom(depName); imported != "" {
			importedCtx = imported + "\n" + importedCtx
		}
	}

	// Load artifact references declared before reasoning.
	for _, ref := range step.ArtifactRefs {
		if !ref.Create && ref.Name != "" {
			content, err := o.store.ReadArtifact(ref.Name)
			if err == nil {
				importedCtx = fmt.Sprintf("[artifact: %s]\n%s\n\n%s", ref.Name, content, importedCtx)
			}
		}
	}

	// Build step capability list and args from CapCalls.
	stepCapNames, capArgs := buildCapSets(step.CapCalls, use.Scope)

	// If no prompt and no caps — nothing for the agent to do.
	if step.Kind == "" && len(stepCapNames) == 0 {
		return &workflow.StepResult{
			StepName: step.Name,
			Status:   workflow.StepOK,
		}
	}

	// Hand off to agent.
	out, err := ag.RunStep(ctx, step, stepCapNames, capArgs, importedCtx)
	if err != nil {
		return &workflow.StepResult{
			StepName: step.Name,
			Status:   workflow.StepFailed,
			Error:    err,
		}
	}

	// Export if declared.
	if step.Export {
		ag.Export(step.Name, out)
	}

	// Create artifacts declared after write/plan.
	for _, ref := range step.ArtifactRefs {
		if ref.Create && ref.Name != "" && out.Answer != "" {
			kind := "output"
			if step.Kind == workflow.StepPlan {
				kind = "plan"
			} else if step.Kind == workflow.StepReason {
				kind = "analysis"
			}
			_ = o.store.SaveArtifact("", "", step.Name, ref.Name, kind, out.Answer)
		}
	}

	return &workflow.StepResult{
		StepName: step.Name,
		Status:   workflow.StepOK,
		Output:   out.Answer,
	}
}

// runCheckpoint handles a checkpoint in the sequence.
func (o *Orchestrator) runCheckpoint(cp *workflow.CheckpointDef) error {
	o.log.Info("checkpoint", "label", cp.Label, "type", cp.Type)

	if cp.Type == workflow.CheckpointWorktree {
		o.log.Info("worktree — project snapshot before continuing", "label", cp.Label)
		// v0.3: full worktree copy implementation.
	}

	if cp.ReviewFile != "" {
		return o.runReviewGate(cp.ReviewFile)
	}
	return nil
}

// runReviewGate pauses execution and presents an artifact for review.
func (o *Orchestrator) runReviewGate(artifactPath string) error {
	content, err := o.store.ReadArtifact(artifactPath)
	if err != nil {
		o.log.Warn("review gate: artifact not found", "path", artifactPath)
	} else {
		fmt.Println()
		fmt.Printf("  ── review: %s ──────────────────────────\n\n", artifactPath)
		for _, line := range strings.Split(content, "\n") {
			fmt.Printf("  %s\n", line)
		}
		fmt.Println()
	}

	fmt.Print("  continue? [y/n]: ")
	var response string
	fmt.Scanln(&response)
	if strings.ToLower(strings.TrimSpace(response)) != "y" {
		return fmt.Errorf("review gate: rejected at %q", artifactPath)
	}
	return nil
}

// ── Helpers ────────────────────────────────────────────────────────────────

// buildCapSets builds the step's capability name list and args map from CapCalls.
// If all_capabilities() is declared, expands to everything in scope.
func buildCapSets(calls []workflow.CapCall, scope *workflow.Scope) ([]string, map[string]map[string]string) {
	args := make(map[string]map[string]string)
	seen := make(map[string]bool)
	var names []string

	for _, c := range calls {
		if c.All {
			// all_capabilities() — use everything in scope.
			if scope != nil {
				for _, name := range scope.Capabilities {
					if !seen[name] {
						names = append(names, name)
						seen[name] = true
					}
				}
			}
			continue
		}
		if c.Name == "" {
			continue
		}
		if !seen[c.Name] {
			names = append(names, c.Name)
			seen[c.Name] = true
		}
		// Build args map from CapCall.Args.
		// Single arg: interpret as the primary field (pattern, path, cmd).
		if len(c.Args) > 0 {
			if args[c.Name] == nil {
				args[c.Name] = make(map[string]string)
			}
			switch c.Name {
			case "filesystem.glob", "process.watch":
				args[c.Name]["pattern"] = c.Args[0]
			case "filesystem.read", "filesystem.write", "filesystem.edit":
				args[c.Name]["path"] = c.Args[0]
			case "process.execute", "process.background":
				// Multiple args = multiple locked commands.
				// For now, join as semicolons if multiple.
				args[c.Name]["cmd"] = strings.Join(c.Args, " && ")
			default:
				args[c.Name]["value"] = c.Args[0]
			}
		}
	}

	return names, args
}

func findStep(steps []*workflow.Step, name string) *workflow.Step {
	for _, s := range steps {
		if s.Name == name {
			return s
		}
	}
	return nil
}

func countTasks(seq []workflow.SequenceItem) int {
	n := 0
	for _, item := range seq {
		if item.Kind == workflow.SeqTask {
			n++
		}
	}
	return n
}
