// Package orchestrator executes Loom workflow files.
// It drives the sequence: tasks, checkpoints, clear, clean, finish.
// Every capability call passes through the Guard.
// Independent steps at the same graph depth run in parallel.
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
	"github.com/dominionthedev/loom/internal/guard"
	"github.com/dominionthedev/loom/internal/model"
	"github.com/dominionthedev/loom/internal/storage"
	"github.com/dominionthedev/loom/internal/workflow"
)

// Orchestrator executes a workflow File.
type Orchestrator struct {
	caps   *capability.Registry
	guard  *guard.Guard
	router *model.Router
	store  *storage.Store
	log    *log.Logger
}

// New creates an Orchestrator.
func New(
	caps *capability.Registry,
	router *model.Router,
	store *storage.Store,
	logger *log.Logger,
) *Orchestrator {
	return &Orchestrator{
		caps:   caps,
		guard:  guard.New(caps),
		router: router,
		store:  store,
		log:    logger,
	}
}

// Run executes a workflow file's sequence.
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
			if err := o.runCheckpoint(ctx, item.Checkpoint); err != nil {
				result.Error = err
				goto done
			}

		case workflow.SeqClear:
			o.log.Info("clear")
			// v0.1: clear is a no-op at runtime level — task residue is ephemeral.

		case workflow.SeqClean:
			o.log.Info("clean")
			// v0.4: full session management. v0.1: no-op.

		case workflow.SeqFinish:
			o.log.Info("finish — workflow complete")
		}
	}

done:
	o.store.RecordRun(storage.RunRecord{
		RunID:   runID,
		File:    "",
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

	// Build execution graph.
	g, err := graph.Build(task.Steps)
	if err != nil {
		result.Error = fmt.Errorf("task %s: %w", task.Name, err)
		return result
	}

	// One agent per task.
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
				fmt.Fprintf(&stepContext, "[%s] %s\n", sr.StepName, sr.Output)
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

// execLevel runs all steps in a graph level, in parallel if more than one.
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

// execStep dispatches a single step.
func (o *Orchestrator) execStep(
	ctx context.Context,
	use *workflow.UseConfig,
	step *workflow.Step,
	ag *agent.Agent,
	accumulatedContext string,
) *workflow.StepResult {

	o.log.Info("step", "name", step.Name, "kind", step.Kind)

	// Import exported context from depends_on.
	for _, depName := range step.DependsOn {
		if imported := ag.ImportFrom(depName); imported != "" {
			accumulatedContext = fmt.Sprintf("[imported from %s]\n%s\n\n%s", depName, imported, accumulatedContext)
		}
	}

	// Load artifact references declared before reasoning.
	for _, ref := range step.ArtifactRefs {
		if !ref.Create && ref.Name != "" {
			content, err := o.store.ReadArtifact(ref.Name)
			if err == nil {
				accumulatedContext = fmt.Sprintf("[artifact: %s]\n%s\n\n%s", ref.Name, content, accumulatedContext)
			}
		}
	}

	var output string
	var stepErr error

	// ── Reasoning ──────────────────────────────────────────────────────
	switch step.Kind {
	case workflow.StepReason:
		output, stepErr = ag.Reason(ctx, step.Name, step.Prompt, step.ThinkLevel, accumulatedContext)
		if stepErr != nil {
			return failed(step.Name, stepErr)
		}

	case workflow.StepPlan:
		output, stepErr = ag.Plan(ctx, step.Name, step.Prompt, accumulatedContext)
		if stepErr != nil {
			return failed(step.Name, stepErr)
		}
	}

	// ── Capability execution ────────────────────────────────────────────
	for _, call := range step.CapCalls {
		if call.All {
			// all_capabilities(): run all scope capabilities that have args
			continue
		}

		capName := call.Name
		if capName == "" {
			continue
		}

		// Build input from call args.
		input := capability.Input{}
		if len(call.Args) == 1 {
			// Single arg: treat as cmd or path depending on capability.
			if strings.HasPrefix(capName, "process.") {
				input["cmd"] = call.Args[0]
			} else {
				input["path"] = call.Args[0]
			}
		} else if len(call.Args) > 1 {
			// Multiple commands: run each.
			for _, cmd := range call.Args {
				cmdInput := capability.Input{"cmd": cmd}
				if v := o.guard.Check(use.Scope, use.Policies, step.CapCalls, capName, cmdInput); v != nil {
					o.log.Warn("guard blocked", "step", step.Name, "cap", capName, "reason", v.Message)
					return &workflow.StepResult{
						StepName: step.Name,
						Status:   workflow.StepBlocked,
						Error:    v,
					}
				}
				result := o.caps.Execute(ctx, capName, cmdInput)
				if result.Error != nil && step.OnFailure == workflow.OnFailureStop {
					return failed(step.Name, fmt.Errorf("%s: %w", capName, result.Error))
				}
				output += result.Output + "\n"
			}
			continue
		}

		// Guard check.
		if v := o.guard.Check(use.Scope, use.Policies, step.CapCalls, capName, input); v != nil {
			o.log.Warn("guard blocked", "step", step.Name, "cap", capName, "reason", v.Message)
			return &workflow.StepResult{
				StepName: step.Name,
				Status:   workflow.StepBlocked,
				Error:    v,
			}
		}

		result := o.caps.Execute(ctx, capName, input)
		if result.Error != nil {
			if step.OnFailure == workflow.OnFailureContinue {
				o.log.Warn("step error (continuing)", "step", step.Name, "err", result.Error)
				continue
			}
			return failed(step.Name, fmt.Errorf("%s: %w", capName, result.Error))
		}
		output += result.Output
	}

	// ── Export ─────────────────────────────────────────────────────────
	if step.Export && output != "" {
		ag.Export(step.Name, output)
	}

	// ── Create artifacts ────────────────────────────────────────────────
	for _, ref := range step.ArtifactRefs {
		if ref.Create && ref.Name != "" && output != "" {
			kind := "output"
			if step.Kind == workflow.StepPlan {
				kind = "plan"
			} else if step.Kind == workflow.StepReason {
				kind = "analysis"
			}
			_ = o.store.SaveArtifact("", "", step.Name, ref.Name, kind, output)
		}
	}

	return &workflow.StepResult{
		StepName: step.Name,
		Status:   workflow.StepOK,
		Output:   output,
	}
}

// runCheckpoint handles a checkpoint in the sequence.
func (o *Orchestrator) runCheckpoint(ctx context.Context, cp *workflow.CheckpointDef) error {
	o.log.Info("checkpoint", "label", cp.Label, "type", cp.Type)

	if cp.Type == workflow.CheckpointWorktree {
		o.log.Info("worktree checkpoint — snapshot before continuing", "label", cp.Label)
		// v0.3: full worktree copy implementation.
		// v0.1: log intent, continue.
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
		// Non-fatal: artifact may not exist yet.
		o.log.Warn("review gate: artifact not found", "path", artifactPath)
	} else {
		fmt.Println()
		fmt.Printf("  ── review: %s ──────────────────────────────\n", artifactPath)
		fmt.Println()
		lines := strings.Split(content, "\n")
		for _, line := range lines {
			fmt.Printf("  %s\n", line)
		}
		fmt.Println()
	}

	fmt.Print("  continue? [y/n]: ")
	var response string
	fmt.Scanln(&response)
	response = strings.TrimSpace(strings.ToLower(response))

	if response != "y" && response != "yes" {
		return fmt.Errorf("review gate: rejected at %q", artifactPath)
	}
	return nil
}

// ── Helpers ────────────────────────────────────────────────────────────────

func failed(stepName string, err error) *workflow.StepResult {
	return &workflow.StepResult{
		StepName: stepName,
		Status:   workflow.StepFailed,
		Error:    err,
	}
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


// Ensure os is used.
