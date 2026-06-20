// Package orchestrator executes Loom workflow files.
// The agent drives each step. The orchestrator manages the sequence,
// builds tool sets, and wires context between steps.
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
	caps    *capability.Registry
	router  *model.Router
	store   *storage.Store
	log     *log.Logger
	verbose bool
	grd     *guard.Guard
}

// New creates an Orchestrator.
func New(caps *capability.Registry, router *model.Router, store *storage.Store, logger *log.Logger, verbose bool) *Orchestrator {
	return &Orchestrator{
		caps:    caps,
		router:  router,
		store:   store,
		log:     logger,
		verbose: verbose,
		grd:     guard.New(caps),
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

	// Verbose log function wired to charmbracelet/log.
	var logFn agent.LogFunc
	if o.verbose {
		logFn = func(event, detail string) {
			o.log.Debug(event, "detail", detail)
		}
	}

	// One agent per task, locked to its use() config.
	ag := agent.New(use, o.caps, o.store, o.router, logFn)

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

			if (sr.Status == workflow.StepFailed || sr.Status == workflow.StepBlocked) && sr.Error != nil {
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

	// Surface capabilities the agent needed but didn't have access to —
	// non-blocking, helps catch a missing scope/step declaration.
	for _, d := range ag.Denied() {
		result.Denied = append(result.Denied, workflow.DeniedCapability{
			Step:   d.Step,
			Tool:   d.Tool,
			Reason: d.Reason,
		})
	}

	return result
}

// execLevel runs steps at one graph level, in parallel if more than one.
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

// execStep dispatches one step.
//
// Two modes:
//
//  1. Capability-only, fully specified (all caps have args) — run directly,
//     no agent reasoning needed. Fast path for deterministic steps like
//     execute("go test ./...").
//
//  2. Everything else — hand to agent loop. Agent decides tool calls.
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

	// Load artifact references.
	for _, ref := range step.ArtifactRefs {
		if !ref.Create && ref.Name != "" {
			content, err := o.store.ReadArtifact(ref.Name)
			if err == nil {
				importedCtx = fmt.Sprintf("[artifact: %s]\n%s\n\n%s", ref.Name, content, importedCtx)
			}
		}
	}

	stepCapNames, capArgs, artifactsScoped := buildCapSets(step.CapCalls, use.Scope)

	// ── Fast path: capability-only, all args specified ─────────────────
	// No reasoning needed — run caps directly and return.
	if step.Kind == "" && allCapsSpecified(step.CapCalls, capArgs) {
		return o.execCapsDirectly(ctx, use, step, stepCapNames, capArgs, ag, importedCtx)
	}

	// ── Nothing to do ──────────────────────────────────────────────────
	// No reasoning, no capabilities — but artifact refs may have already
	// loaded content into importedCtx (e.g. a pure "load this artifact"
	// bridge step between tasks). Still export it if asked, or depends_on
	// in the next task silently loses that context.
	if step.Kind == "" && len(stepCapNames) == 0 {
		if step.Export && importedCtx != "" {
			ag.Export(step.Name, &agent.StepOutput{Context: importedCtx})
		}
		return &workflow.StepResult{StepName: step.Name, Status: workflow.StepOK}
	}

	// ── Agent path: hand off to agent loop ─────────────────────────────
	out, err := ag.RunStep(ctx, step, stepCapNames, capArgs, artifactsScoped, importedCtx)
	if err != nil {
		return &workflow.StepResult{
			StepName: step.Name,
			Status:   workflow.StepFailed,
			Error:    err,
		}
	}

	if step.Export {
		ag.Export(step.Name, out)
	}

	// Create artifacts after write/plan.
	// Defense in depth: even with the parser fix for glued tool-call text,
	// don't silently save something that still looks like a dangling,
	// incomplete tool-call fragment as if it were a real, finished answer.
	// A bad artifact poisons every downstream step that loads it.
	for _, ref := range step.ArtifactRefs {
		if ref.Create && ref.Name != "" && out.Answer != "" {
			if reason := malformedAnswerReason(out.Answer); reason != "" {
				return &workflow.StepResult{
					StepName: step.Name,
					Status:   workflow.StepFailed,
					Error: fmt.Errorf(
						"step %q produced a malformed final answer (%s) — refusing to save as artifact %q",
						step.Name, reason, ref.Name,
					),
				}
			}
			kind := kindOf(step.Kind)
			_ = o.store.SaveArtifact("", "", step.Name, ref.Name, kind, out.Answer)
		}
	}

	return &workflow.StepResult{
		StepName: step.Name,
		Status:   workflow.StepOK,
		Output:   out.Answer,
	}
}

// execCapsDirectly runs fully-specified capability-only steps without the agent.
// Example: execute("go test ./...") — nothing to reason about, just run it.
// Still passes through Guard — fully-specified doesn't mean unguarded.
func (o *Orchestrator) execCapsDirectly(
	ctx context.Context,
	use *workflow.UseConfig,
	step *workflow.Step,
	capNames []string,
	capArgs map[string]map[string]string,
	ag *agent.Agent,
	importedCtx string,
) *workflow.StepResult {

	var outputParts []string

	for _, name := range capNames {
		// Only run caps that have all fields covered by args.
		if _, hasArgs := capArgs[name]; !hasArgs {
			continue
		}
		input := capability.Input(capArgs[name])

		if v := o.grd.Check(use.Scope, use.Policies, capNames, name, input, false); v != nil {
			o.log.Warn("guard blocked", "step", step.Name, "cap", name, "reason", v.Message)
			ag.RecordDenied(step.Name, name, v.Message)
			return &workflow.StepResult{
				StepName: step.Name,
				Status:   workflow.StepBlocked,
				Error:    v,
			}
		}

		result := o.caps.Execute(ctx, name, input)
		if result.Error != nil {
			if step.OnFailure == workflow.OnFailureContinue {
				o.log.Warn("cap error (continuing)", "cap", name, "err", result.Error)
				continue
			}
			return &workflow.StepResult{
				StepName: step.Name,
				Status:   workflow.StepFailed,
				Error:    fmt.Errorf("%s: %w", name, result.Error),
			}
		}
		if result.Output != "" {
			outputParts = append(outputParts, result.Output)
		}
	}

	output := strings.Join(outputParts, "\n")

	if step.Export && output != "" {
		out := &agent.StepOutput{Answer: output, Context: output}
		ag.Export(step.Name, out)
	}

	return &workflow.StepResult{
		StepName: step.Name,
		Status:   workflow.StepOK,
		Output:   output,
	}
}

// allCapsSpecified returns true if every non-all CapCall has args.
func allCapsSpecified(calls []workflow.CapCall, capArgs map[string]map[string]string) bool {
	for _, c := range calls {
		if c.All {
			return false // all_capabilities() always needs agent
		}
		if c.ArtifactsScope {
			return false // agent must choose which artifact file by name
		}
		if _, hasArgs := capArgs[c.Name]; !hasArgs {
			return false // this cap needs agent input
		}
	}
	return len(calls) > 0
}

// runCheckpoint handles a checkpoint.
func (o *Orchestrator) runCheckpoint(cp *workflow.CheckpointDef) error {
	o.log.Info("checkpoint", "label", cp.Label, "type", cp.Type)
	if cp.Type == workflow.CheckpointWorktree {
		o.log.Info("worktree snapshot", "label", cp.Label)
	}
	if cp.ReviewFile != "" {
		return o.runReviewGate(cp.ReviewFile)
	}
	return nil
}

// runReviewGate pauses and shows an artifact for review.
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

// buildCapSets builds the capability name list, args map, and the
// artifacts-scoped set from CapCalls.
func buildCapSets(calls []workflow.CapCall, scope *workflow.Scope) ([]string, map[string]map[string]string, map[string]bool) {
	args := make(map[string]map[string]string)
	artifactsScoped := make(map[string]bool)
	seen := make(map[string]bool)
	var names []string

	for _, c := range calls {
		if c.All {
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

		if c.ArtifactsScope {
			// Domain restriction, not a literal value — the agent still
			// chooses the filename. Don't set a "path" arg; executeTool
			// resolves it relative to .loom/artifacts/ at call time.
			artifactsScoped[c.Name] = true
			continue
		}

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
				if len(c.Args) == 1 {
					args[c.Name]["cmd"] = c.Args[0]
				} else {
					args[c.Name]["cmd"] = strings.Join(c.Args, " && ")
				}
			default:
				args[c.Name]["value"] = c.Args[0]
			}
		}
	}

	return names, args, artifactsScoped
}

// malformedAnswerReason returns a non-empty reason if answer looks like a
// dangling tool-call fragment rather than a genuine finished response —
// e.g. the model ran out of useful moves and its last reply happened not
// to match the tool-call format, so it was treated as "final" by accident.
// Empty return means the answer looks fine to save.
func malformedAnswerReason(answer string) string {
	if strings.Contains(answer, "TOOL:") && strings.Contains(answer, "INPUT:") {
		return "contains a stray TOOL:/INPUT: fragment"
	}
	trimmed := strings.TrimSpace(answer)
	if len(trimmed) < 10 {
		return "suspiciously short"
	}
	return ""
}

func kindOf(k workflow.StepKind) string {
	switch k {
	case workflow.StepPlan:
		return "plan"
	case workflow.StepReason:
		return "analysis"
	default:
		return "output"
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
