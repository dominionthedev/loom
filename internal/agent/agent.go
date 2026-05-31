// Package agent provides Loom's constrained reasoning layer.
// The agent IS the execution — it reasons, calls tools, and produces output.
// It operates inside boundaries defined by use(): scope, capabilities, rules.
//
// The agent loop per step:
//
//	Receive: prompt + context + available tools (with effective schemas)
//	Loop:
//	  model responds with tool call OR final answer
//	  tool call → runtime executes → result fed back → continue
//	  final answer → step output = answer + accumulated tool outputs
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

const maxToolCallIterations = 15

// ToolCall is a parsed tool invocation from the model's response.
type ToolCall struct {
	Tool  string
	Input capability.Input
}

// StepOutput is what a step produces after the agent loop completes.
type StepOutput struct {
	Answer      string            // agent's final text output
	ToolOutputs map[string]string // tool name → last output from that tool
	Context     string            // full context string for export/depends_on
}

// Agent is a constrained reasoning instance, one per task.
type Agent struct {
	def             *workflow.AgentDef
	use             *workflow.UseConfig
	caps            *capability.Registry
	store           *storage.Store
	router          *model.Router
	history         []model.Turn
	exportedContext map[string]string // step name → exported context
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
		def = &workflow.AgentDef{Name: "default", ThinkLevel: "low"}
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

// RunStep executes a step's agent loop.
// stepCaps is the list of capabilities available in this step (from CapCalls).
// capArgs is a map of capName → static args from the Lua file.
// accumulatedContext is the context from prior steps.
func (a *Agent) RunStep(
	ctx context.Context,
	step *workflow.Step,
	stepCapNames []string,
	capArgs map[string]map[string]string,
	accumulatedContext string,
) (*StepOutput, error) {

	m := a.modelForLevel(step.ThinkLevel)
	// plan() always uses high — override.
	if step.Kind == workflow.StepPlan {
		m = a.router.For("high")
	}

	system := a.buildSystem(step, stepCapNames, capArgs)
	toolOutputs := make(map[string]string)

	// Build initial user message from prompt + context.
	userMsg := a.buildUserMessage(step, accumulatedContext)

	// Seed the per-step conversation.
	stepHistory := make([]model.Turn, len(a.history))
	copy(stepHistory, a.history)

	for i := 0; i < maxToolCallIterations; i++ {
		reply, err := m.Chat(ctx, system, stepHistory, userMsg)
		if err != nil {
			return nil, fmt.Errorf("agent: step %q iter %d: %w", step.Name, i, err)
		}

		// Append this exchange to step history.
		stepHistory = append(stepHistory,
			model.Turn{Role: "user", Content: userMsg},
			model.Turn{Role: "assistant", Content: reply},
		)

		// Check if this is a tool call.
		tc := parseToolCall(reply)
		if tc == nil {
			// Final answer — done.
			out := &StepOutput{
				Answer:      reply,
				ToolOutputs: toolOutputs,
			}
			out.Context = buildContextString(step.Name, reply, toolOutputs, accumulatedContext)

			// Persist step history to agent history for cross-step continuity.
			a.history = stepHistory
			return out, nil
		}

		// Execute the tool call.
		toolResult := a.executeTool(ctx, tc, capArgs, stepCapNames)
		toolOutputs[tc.Tool] = toolResult

		// Feed result back as next user message.
		userMsg = fmt.Sprintf("Tool result for %s:\n%s\n\nContinue.", tc.Tool, toolResult)
	}

	return nil, fmt.Errorf("agent: step %q exceeded max iterations (%d)", step.Name, maxToolCallIterations)
}

// executeTool runs a tool call from the agent.
// Merges static args (from Lua) with agent-provided input.
// Static args always win — they cannot be overridden by the agent.
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

	// Merge: static args override agent input.
	staticArgs := capability.Input(capArgs[tc.Tool])
	merged := staticArgs.Merge(tc.Input)

	result := a.caps.Execute(ctx, tc.Tool, merged)
	if result.Error != nil {
		return fmt.Sprintf("error: %v", result.Error)
	}
	return result.Output
}

// Export marks a step's context as available for downstream depends_on imports.
// Requires memory() to be initialized for cross-task persistence.
func (a *Agent) Export(stepName string, out *StepOutput) {
	a.exportedContext[stepName] = out.Context
}

// ImportFrom returns the exported context from a prior step.
func (a *Agent) ImportFrom(stepName string) string {
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

	// Scope boundaries.
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

	// Rules.
	if len(a.use.Rules) > 0 {
		sb.WriteString("Rules — you must follow these:\n")
		for _, r := range a.use.Rules {
			for _, c := range r.Constraints {
				sb.WriteString(fmt.Sprintf("  - %s\n", c))
			}
		}
		sb.WriteString("\n")
	}

	// Available tools with effective schemas.
	if len(stepCapNames) > 0 {
		sb.WriteString("Available tools:\n")
		sb.WriteString(a.caps.DescribeForAgent(stepCapNames, capArgs))
		sb.WriteString("\n")
	}

	// Tool call format.
	sb.WriteString(`When you need to use a tool, respond with EXACTLY this format and nothing else:
TOOL: tool_name
INPUT: key1=value1 key2=value2

For multi-line values use key=<<<
value here
>>>

When you are finished and have your final answer, respond with plain text only (no TOOL: prefix).
Do not explain your tool calls — just make them. Be precise.
`)

	// Mode-specific.
	switch step.Kind {
	case workflow.StepPlan:
		sb.WriteString("\nYou are producing a structured implementation plan.\n")
		sb.WriteString("Format: numbered steps, file paths, exact changes. Be specific.\n")
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
		sb.WriteString("Execute this step using the available tools.")
	}

	return sb.String()
}

func (a *Agent) modelForLevel(level string) model.Model {
	if level == "" && a.def.ThinkLevel != "" {
		level = a.def.ThinkLevel
	}
	return a.router.For(level)
}

// buildContextString assembles the full context for export().
func buildContextString(stepName, answer string, toolOutputs map[string]string, priorContext string) string {
	var sb strings.Builder
	if priorContext != "" {
		sb.WriteString(priorContext)
		sb.WriteString("\n")
	}
	if len(toolOutputs) > 0 {
		for tool, out := range toolOutputs {
			sb.WriteString(fmt.Sprintf("[%s → %s]\n%s\n", stepName, tool, truncate(out, 2000)))
		}
	}
	if answer != "" {
		sb.WriteString(fmt.Sprintf("[%s reasoning]\n%s\n", stepName, answer))
	}
	return sb.String()
}

// ── Tool call parsing ──────────────────────────────────────────────────────

// parseToolCall attempts to parse a model response as a tool call.
// Returns nil if the response is a final answer (no TOOL: prefix).
func parseToolCall(response string) *ToolCall {
	response = strings.TrimSpace(response)
	if !strings.HasPrefix(response, "TOOL:") {
		return nil
	}

	lines := strings.Split(response, "\n")
	tc := &ToolCall{Input: make(capability.Input)}

	for i, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "TOOL:") {
			tc.Tool = strings.TrimSpace(strings.TrimPrefix(line, "TOOL:"))
			continue
		}
		if strings.HasPrefix(line, "INPUT:") {
			inputStr := strings.TrimSpace(strings.TrimPrefix(line, "INPUT:"))
			parseInputLine(inputStr, tc.Input)

			// Handle multi-line values with <<<...>>>
			for j := i + 1; j < len(lines); j++ {
				rest := strings.TrimSpace(lines[j])
				if rest == "" {
					continue
				}
				parseInputLine(rest, tc.Input)
			}
			break
		}
	}

	if tc.Tool == "" {
		return nil
	}
	return tc
}

// parseInputLine parses "key=value" or "key=<<<" multi-line markers.
func parseInputLine(line string, input capability.Input) {
	// Handle key=<<<multi-line>>> patterns by extracting inline content.
	eqIdx := strings.Index(line, "=")
	if eqIdx < 0 {
		return
	}
	key := strings.TrimSpace(line[:eqIdx])
	val := strings.TrimSpace(line[eqIdx+1:])

	// Strip <<< >>> markers if present.
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
