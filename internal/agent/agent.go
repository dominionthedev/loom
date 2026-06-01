// Package agent provides Loom's constrained reasoning layer.
// The agent IS the execution — it reasons, calls tools, and produces output.
// One agent instance per task. Parallel steps get snapshot copies of history.
package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/dominionthedev/loom/internal/capability"
	"github.com/dominionthedev/loom/internal/model"
	"github.com/dominionthedev/loom/internal/storage"
	"github.com/dominionthedev/loom/internal/workflow"
)

const maxToolCallIterations = 15

// ToolCall is a parsed tool invocation from the model's response.
type ToolCall struct {
	Tool  string
	Input capability.Input
}

// StepOutput is what a step produces after the agent loop completes.
type StepOutput struct {
	Answer      string
	ToolOutputs map[string]string // tool name → last output
	Context     string            // full context string for export/depends_on
}

// LogFunc is called for verbose output during execution.
// nil = silent.
type LogFunc func(event, detail string)

// Agent is a constrained reasoning instance, one per task.
type Agent struct {
	def             *workflow.AgentDef
	use             *workflow.UseConfig
	caps            *capability.Registry
	store           *storage.Store
	router          *model.Router
	log             LogFunc

	mu              sync.RWMutex
	history         []model.Turn
	exportedContext map[string]string // step name → exported context
}

// New creates an Agent for a task.
func New(
	use *workflow.UseConfig,
	caps *capability.Registry,
	store *storage.Store,
	router *model.Router,
	logFn LogFunc,
) *Agent {
	def := use.Agent
	if def == nil {
		def = &workflow.AgentDef{Name: "default", ThinkLevel: "low"}
	}
	return &Agent{
		def:             def,
		use:             use,
		caps:            caps,
		store:           store,
		router:          router,
		log:             logFn,
		exportedContext: make(map[string]string),
	}
}

// RunStep executes a step's agent loop.
// For parallel steps, a snapshot of history is used — thread-safe.
func (a *Agent) RunStep(
	ctx context.Context,
	step *workflow.Step,
	stepCapNames []string,
	capArgs map[string]map[string]string,
	accumulatedContext string,
) (*StepOutput, error) {

	m := a.modelForLevel(step.ThinkLevel)
	if step.Kind == workflow.StepPlan {
		m = a.router.For("high")
	}

	system := a.buildSystem(step, stepCapNames, capArgs)
	toolOutputs := make(map[string]string)

	// Snapshot history for this step — safe for parallel execution.
	a.mu.RLock()
	stepHistory := make([]model.Turn, len(a.history))
	copy(stepHistory, a.history)
	a.mu.RUnlock()

	userMsg := a.buildUserMessage(step, accumulatedContext)

	for i := 0; i < maxToolCallIterations; i++ {
		if a.log != nil {
			a.log("agent:thinking", fmt.Sprintf("step=%s iter=%d", step.Name, i+1))
		}

		reply, err := m.Chat(ctx, system, stepHistory, userMsg)
		if err != nil {
			return nil, fmt.Errorf("agent: step %q: %w", step.Name, err)
		}

		if a.log != nil {
			a.log("agent:reply", truncate(reply, 200))
		}

		stepHistory = append(stepHistory,
			model.Turn{Role: "user", Content: userMsg},
			model.Turn{Role: "assistant", Content: reply},
		)

		tc := parseToolCall(reply)
		if tc == nil {
			// Final answer.
			out := &StepOutput{
				Answer:      reply,
				ToolOutputs: toolOutputs,
			}
			out.Context = buildContextString(step.Name, reply, toolOutputs, accumulatedContext)

			// Merge step history back into agent history (under lock).
			a.mu.Lock()
			a.history = stepHistory
			a.mu.Unlock()

			return out, nil
		}

		// Execute the tool.
		toolResult := a.executeTool(ctx, tc, capArgs, stepCapNames)
		toolOutputs[tc.Tool] = toolResult

		if a.log != nil {
			a.log("tool:result", fmt.Sprintf("%s → %s", tc.Tool, truncate(toolResult, 200)))
		}

		userMsg = fmt.Sprintf("Tool result for %s:\n%s\n\nContinue.", tc.Tool, toolResult)
	}

	return nil, fmt.Errorf("agent: step %q exceeded max iterations (%d)", step.Name, maxToolCallIterations)
}

// executeTool runs a tool call from the agent.
// Static args always override agent-provided input.
func (a *Agent) executeTool(
	ctx context.Context,
	tc *ToolCall,
	capArgs map[string]map[string]string,
	allowedCaps []string,
) string {
	// Verify tool is in allowed set.
	allowed := false
	for _, name := range allowedCaps {
		if name == tc.Tool {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Sprintf("error: tool %q not available in this step", tc.Tool)
	}

	staticArgs := capability.Input(capArgs[tc.Tool])
	merged := staticArgs.Merge(tc.Input)

	if a.log != nil {
		a.log("tool:call", fmt.Sprintf("%s %v", tc.Tool, merged))
	}

	result := a.caps.Execute(ctx, tc.Tool, merged)
	if result.Error != nil {
		return fmt.Sprintf("error: %v", result.Error)
	}
	return result.Output
}

// Export marks a step's full context as available downstream.
func (a *Agent) Export(stepName string, out *StepOutput) {
	a.mu.Lock()
	a.exportedContext[stepName] = out.Context
	a.mu.Unlock()
}

// ImportFrom returns exported context from a prior step.
func (a *Agent) ImportFrom(stepName string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.exportedContext[stepName]
}

// buildSystem constructs the agent's system prompt for a step.
func (a *Agent) buildSystem(
	step *workflow.Step,
	stepCapNames []string,
	capArgs map[string]map[string]string,
) string {
	var sb strings.Builder

	sb.WriteString("You are an AI agent operating inside Loom, a programmable workflow runtime.\n")
	sb.WriteString("You execute tasks by reasoning and using tools.\n\n")

	if a.def.System != "" {
		sb.WriteString(a.def.System)
		sb.WriteString("\n\n")
	}

	if a.use.Scope != nil {
		sb.WriteString(fmt.Sprintf("Scope: %s\n", a.use.Scope.Name))
		if len(a.use.Scope.Include) > 0 {
			sb.WriteString(fmt.Sprintf("  visible paths: %s\n", strings.Join(a.use.Scope.Include, ", ")))
		}
		if len(a.use.Scope.Exclude) > 0 {
			sb.WriteString(fmt.Sprintf("  excluded paths: %s\n", strings.Join(a.use.Scope.Exclude, ", ")))
		}
		sb.WriteString("\n")
	}

	if len(a.use.Rules) > 0 {
		sb.WriteString("Rules — you must follow these:\n")
		for _, r := range a.use.Rules {
			for _, c := range r.Constraints {
				sb.WriteString(fmt.Sprintf("  - %s\n", c))
			}
		}
		sb.WriteString("\n")
	}

	if len(stepCapNames) > 0 {
		sb.WriteString("Available tools:\n")
		sb.WriteString(a.caps.DescribeForAgent(stepCapNames, capArgs))
		sb.WriteString("\n")
	}

	sb.WriteString(`When you need to use a tool, respond with EXACTLY this format and nothing else:
TOOL: tool_name
INPUT: key1=value1 key2=value2

For multi-line content use:
TOOL: tool_name
INPUT: key=<<<
content here
>>>

When finished, respond with plain text only (no TOOL: prefix).
Do not explain tool calls. Be precise and minimal.
`)

	switch step.Kind {
	case workflow.StepPlan:
		sb.WriteString("\nYou are producing a structured implementation plan.\n")
		sb.WriteString("Format: numbered steps, file paths, exact changes. No vagueness.\n")
	case workflow.StepReason:
		sb.WriteString("\nReason precisely. Do not hallucinate file contents or tool outputs.\n")
	}

	return sb.String()
}

func (a *Agent) buildUserMessage(step *workflow.Step, accumulatedContext string) string {
	var sb strings.Builder
	if accumulatedContext != "" {
		sb.WriteString("Context from previous steps:\n")
		sb.WriteString(accumulatedContext)
		sb.WriteString("\n\n")
	}
	if step.Prompt != "" {
		sb.WriteString(step.Prompt)
	} else {
		sb.WriteString("Complete this step using the available tools.")
	}
	return sb.String()
}

func (a *Agent) modelForLevel(level string) model.Model {
	if level == "" && a.def.ThinkLevel != "" {
		level = a.def.ThinkLevel
	}
	return a.router.For(level)
}

func buildContextString(stepName, answer string, toolOutputs map[string]string, priorContext string) string {
	var sb strings.Builder
	if priorContext != "" {
		sb.WriteString(priorContext)
		sb.WriteString("\n")
	}
	for tool, out := range toolOutputs {
		sb.WriteString(fmt.Sprintf("[%s → %s]\n%s\n", stepName, tool, truncate(out, 2000)))
	}
	if answer != "" {
		sb.WriteString(fmt.Sprintf("[%s]\n%s\n", stepName, answer))
	}
	return sb.String()
}

// ── Tool call parsing ──────────────────────────────────────────────────────

func parseToolCall(response string) *ToolCall {
	response = strings.TrimSpace(response)
	if !strings.HasPrefix(response, "TOOL:") {
		return nil
	}

	tc := &ToolCall{Input: make(capability.Input)}
	lines := strings.Split(response, "\n")
	inMultiline := false
	var mlKey, mlVal strings.Builder

	for _, line := range lines {
		if strings.HasPrefix(line, "TOOL:") {
			tc.Tool = strings.TrimSpace(strings.TrimPrefix(line, "TOOL:"))
			continue
		}
		if strings.HasPrefix(line, "INPUT:") {
			rest := strings.TrimSpace(strings.TrimPrefix(line, "INPUT:"))
			if rest != "" {
				parseKV(rest, tc.Input, &inMultiline, &mlKey, &mlVal)
			}
			continue
		}
		if inMultiline {
			if strings.TrimSpace(line) == ">>>" {
				tc.Input[mlKey.String()] = strings.TrimSpace(mlVal.String())
				mlKey.Reset()
				mlVal.Reset()
				inMultiline = false
			} else {
				mlVal.WriteString(line)
				mlVal.WriteString("\n")
			}
			continue
		}
		// Continuation key=value lines after INPUT:
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && strings.Contains(trimmed, "=") {
			parseKV(trimmed, tc.Input, &inMultiline, &mlKey, &mlVal)
		}
	}

	if tc.Tool == "" {
		return nil
	}
	return tc
}

func parseKV(line string, input capability.Input, inML *bool, mlKey, mlVal *strings.Builder) {
	eqIdx := strings.Index(line, "=")
	if eqIdx < 0 {
		return
	}
	key := strings.TrimSpace(line[:eqIdx])
	val := strings.TrimSpace(line[eqIdx+1:])

	if strings.HasSuffix(val, "<<<") || val == "<<<" {
		mlKey.Reset()
		mlKey.WriteString(key)
		mlVal.Reset()
		*inML = true
		return
	}

	val = strings.TrimPrefix(val, "<<<")
	val = strings.TrimSuffix(val, ">>>")
	val = strings.TrimSpace(val)
	if key != "" {
		input[key] = val
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
