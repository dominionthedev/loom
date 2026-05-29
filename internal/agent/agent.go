// Package agent provides Loom's constrained reasoning layer.
// The agent does NOT control execution. It reasons inside boundaries
// defined by the active use() config — scope, capabilities, rules.
// It never acts outside what the workflow declares.
package agent

import (
	"context"
	"fmt"
	"strings"

	"github.com/dominionthedev/loom/internal/capability"
	"github.com/dominionthedev/loom/internal/model"
	"github.com/dominionthedev/loom/internal/storage"
	"github.com/dominionthedev/loom/internal/workflow"
)

// Agent is a constrained reasoning instance.
// One agent is created per task, locked to that task's use() config.
type Agent struct {
	def             *workflow.AgentDef
	use             *workflow.UseConfig
	caps            *capability.Registry
	store           *storage.Store
	router          *model.Router
	history         []model.Turn
	exportedContext map[string]string // step name → exported output
}

// New creates an Agent for a task.
func New(
	use *workflow.UseConfig,
	caps *capability.Registry,
	store *storage.Store,
	router *model.Router,
) *Agent {
	def := use.Agent
	if def == nil {
		def = &workflow.AgentDef{
			Name:       "default",
			ThinkLevel: "low",
		}
	}
	return &Agent{
		def:             def,
		use:             use,
		caps:            caps,
		store:           store,
		router:          router,
		exportedContext: make(map[string]string),
	}
}

// Reason executes a reason() step.
// Output flows into context and can be export()ed for downstream steps.
func (a *Agent) Reason(ctx context.Context, stepName, prompt, thinkLevel, accumulatedContext string) (string, error) {
	m := a.modelForLevel(thinkLevel)
	system := a.buildSystem("reason")

	input := accumulatedContext
	if prompt != "" {
		input = prompt + "\n\nContext:\n" + accumulatedContext
	}

	reply, err := m.Chat(ctx, system, a.history, input)
	if err != nil {
		return "", fmt.Errorf("agent: reason [%s]: %w", stepName, err)
	}

	a.appendHistory(input, reply)
	return reply, nil
}

// Plan executes a plan() step.
// Plan always uses high thinking. Output goes to artifacts, not free context.
func (a *Agent) Plan(ctx context.Context, stepName, prompt, accumulatedContext string) (string, error) {
	m := a.router.For("high")
	system := a.buildSystem("plan")

	input := prompt + "\n\nContext:\n" + accumulatedContext

	reply, err := m.Chat(ctx, system, a.history, input)
	if err != nil {
		return "", fmt.Errorf("agent: plan [%s]: %w", stepName, err)
	}

	a.appendHistory(input, reply)
	return reply, nil
}

// Export marks a step's output as available for downstream depends_on imports.
func (a *Agent) Export(stepName, output string) {
	a.exportedContext[stepName] = output
}

// ImportFrom returns exported context from a dependency step.
func (a *Agent) ImportFrom(stepName string) string {
	return a.exportedContext[stepName]
}

// buildSystem constructs the agent's system prompt.
func (a *Agent) buildSystem(mode string) string {
	var sb strings.Builder

	sb.WriteString("You are the reasoning layer of Loom, a programmable AI workflow runtime.\n")
	sb.WriteString("You do NOT control execution. You reason and respond with structured analysis.\n")
	sb.WriteString("You operate strictly inside the boundaries defined by the current workflow.\n\n")

	if a.def.System != "" {
		sb.WriteString(a.def.System)
		sb.WriteString("\n\n")
	}

	if a.use.Scope != nil && len(a.use.Scope.Capabilities) > 0 {
		sb.WriteString("Capabilities available in this workflow (executed by the runtime, not by you):\n")
		sb.WriteString(a.caps.Describe(a.use.Scope.Capabilities))
		sb.WriteString("\n")
	}

	if a.use.Scope != nil {
		sb.WriteString(fmt.Sprintf("Operational scope: %s\n", a.use.Scope.Name))
		if len(a.use.Scope.Include) > 0 {
			sb.WriteString(fmt.Sprintf("  include: %s\n", strings.Join(a.use.Scope.Include, ", ")))
		}
		if len(a.use.Scope.Exclude) > 0 {
			sb.WriteString(fmt.Sprintf("  exclude: %s\n", strings.Join(a.use.Scope.Exclude, ", ")))
		}
		sb.WriteString("\n")
	}

	if len(a.use.Rules) > 0 {
		sb.WriteString("Rules you must follow:\n")
		for _, r := range a.use.Rules {
			for _, c := range r.Constraints {
				sb.WriteString(fmt.Sprintf("  - %s\n", c))
			}
		}
		sb.WriteString("\n")
	}

	switch mode {
	case "plan":
		sb.WriteString("You are producing a structured implementation plan.\n")
		sb.WriteString("Format output clearly: numbered steps, file paths, specific changes.\n")
		sb.WriteString("Be precise. Never be vague.\n")
	case "reason":
		sb.WriteString("Respond with structured reasoning. Be precise and concise.\n")
		sb.WriteString("Do not hallucinate capability calls or file contents.\n")
	}

	return sb.String()
}

func (a *Agent) modelForLevel(thinkLevel string) model.Model {
	if thinkLevel == "" && a.def.ThinkLevel != "" {
		thinkLevel = a.def.ThinkLevel
	}
	return a.router.For(thinkLevel)
}

func (a *Agent) appendHistory(input, reply string) {
	a.history = append(a.history,
		model.Turn{Role: "user", Content: input},
		model.Turn{Role: "assistant", Content: reply},
	)
}
