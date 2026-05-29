// Package workflow defines the core data types for Loom.
// Everything in the runtime speaks these types.
package workflow

// ── Agent ──────────────────────────────────────────────────────────────────

// AgentDef is an agent definition declared in the DSL.
type AgentDef struct {
	Name       string
	Model      string
	System     string
	ThinkLevel string // "low" | "medium" | "high" | explicit model name
}

// ── Scope ──────────────────────────────────────────────────────────────────

// Scope defines the operational boundary for a task.
// The agent and capabilities operate strictly within it.
type Scope struct {
	Name         string
	Include      []string // glob patterns — visible paths
	Exclude      []string // glob patterns — blocked paths
	Capabilities []string // capability names accessible in this scope
}

// AllowsCapability returns true if cap is declared in this scope.
func (s *Scope) AllowsCapability(cap string) bool {
	if s == nil {
		return false
	}
	for _, c := range s.Capabilities {
		if c == cap {
			return true
		}
	}
	return false
}

// ── Policy ─────────────────────────────────────────────────────────────────

// PolicyKind classifies what a policy enforces.
type PolicyKind string

const (
	PolicyDeny  PolicyKind = "deny"  // block matching patterns
	PolicyAllow PolicyKind = "allow" // only allow matching patterns
	PolicyLimit PolicyKind = "limit" // restrict to matching patterns
)

// Policy is a declarative capability restriction rule.
type Policy struct {
	Name   string
	Kind   PolicyKind
	Target string   // capability name this applies to
	Match  []string // patterns to match against
}

// ── Rule ───────────────────────────────────────────────────────────────────

// Rule is a behavioral constraint on the agent.
// Rules shape how the agent reasons and acts — not just what it can execute.
type Rule struct {
	Name        string
	Constraints []string
}

// ── Workspace ──────────────────────────────────────────────────────────────

// WorkspaceDef is a workspace configuration declared in the DSL.
type WorkspaceDef struct {
	Name    string
	Dir     string   // project root, default "./"
	Shell   string   // execution shell, default "/bin/sh"
	Source  []string // primary source patterns
	EnvFile string   // .env file path (lean integration)
}

// ── Script ─────────────────────────────────────────────────────────────────

// ScriptTrigger is when a script fires.
type ScriptTrigger string

const (
	TriggerOnFailure    ScriptTrigger = "on_failure"
	TriggerAfterTask    ScriptTrigger = "after_task"
	TriggerOnCheckpoint ScriptTrigger = "on_checkpoint"
	TriggerBeforeStep   ScriptTrigger = "before_step"
	TriggerAfterStep    ScriptTrigger = "after_step"
)

// ScriptDef is a named hook script registered in the DSL.
type ScriptDef struct {
	Name    string
	Source  string        // path to .lua file (or future: mushmellow workflow)
	Trigger ScriptTrigger
	Require GuardLevel
}

// ── Guard ──────────────────────────────────────────────────────────────────

// GuardLevel controls how strictly a step is protected.
type GuardLevel string

const (
	GuardLow      GuardLevel = "low"      // allow/deny menu
	GuardImportant GuardLevel = "important" // rule enforcement + TouchID/password
	GuardCritical GuardLevel = "critical"  // discipline agent + TouchID + notification + trace
)

// ── Environment ────────────────────────────────────────────────────────────

// UseConfig is what use() wires together for a task.
type UseConfig struct {
	Agent    *AgentDef
	Scope    *Scope
	Policies []*Policy
	Rules    []*Rule
}

// ── Step ───────────────────────────────────────────────────────────────────

// StepKind classifies what a step does.
type StepKind string

const (
	StepReason StepKind = "reason" // free-form reasoning, output → context
	StepPlan   StepKind = "plan"   // structured reasoning, output → artifact
)

// CapCall is a capability invocation declared inside a step.
type CapCall struct {
	Name    string   // capability name e.g. "filesystem.read"
	Args    []string // explicit args (empty = open, policy-filtered)
	All     bool     // all_capabilities() — full scope access
	Custom  bool     // capability("name") — user-defined
}

// ArtifactRef is an artifact reference or creation declaration inside a step.
type ArtifactRef struct {
	Name   string
	Create bool // true = create after write, false = reference before reasoning
}

// Step is a single configured execution unit inside a task.
// Fields reflect declaration order convention:
//
//	depends_on → artifacts(ref) → think → reason/plan →
//	capabilities → export → artifacts(create) → guard → review
type Step struct {
	Name string
	Kind StepKind // reason | plan (empty = capability-only step)

	// Context flow
	DependsOn []string // step names — enforces DAG + imports exported context
	Export    bool     // make this step's context available downstream

	// Reasoning
	Prompt     string
	ThinkLevel string // overrides agent default for this step

	// Capability access
	CapCalls []CapCall

	// Artifacts
	ArtifactRefs []ArtifactRef // in-order: refs before reasoning, creates after

	// Guard + review
	Guard      GuardLevel
	Review     bool   // pause for manual approval
	ReviewAgent string // named agent to review before approval

	// Failure handling
	Retry     int
	OnFailure OnFailure
}

// OnFailure declares what happens when a step fails.
type OnFailure string

const (
	OnFailureStop     OnFailure = "stop"
	OnFailureContinue OnFailure = "continue"
)

// ── Task ───────────────────────────────────────────────────────────────────

// Task is the primary execution unit in Loom.
// A task runs with a specific UseConfig (agent, scope, policies, rules).
type Task struct {
	Name string
	Use  *UseConfig
	Steps []*Step
}

// ── Checkpoint ─────────────────────────────────────────────────────────────

// CheckpointType classifies what a checkpoint creates.
type CheckpointType string

const (
	CheckpointWorktree CheckpointType = "worktree" // copy project to .loom/worktrees/
	CheckpointState    CheckpointType = "state"    // snapshot runtime state only
)

// CheckpointDef is a checkpoint declaration between tasks.
type CheckpointDef struct {
	Label      string
	Type       CheckpointType
	ReviewFile string // artifact path to present for review (empty = no review gate)
}

// ── Memory ─────────────────────────────────────────────────────────────────

// MemoryConfig is the memory initialization config from memory() in the DSL.
type MemoryConfig struct {
	Persist  bool
	Location string // "local" | "global"
	Compress bool
}

// ── Results ────────────────────────────────────────────────────────────────

// StepStatus records how a step completed.
type StepStatus string

const (
	StepOK      StepStatus = "ok"
	StepFailed  StepStatus = "failed"
	StepSkipped StepStatus = "skipped"
	StepBlocked StepStatus = "blocked" // Guard denied execution
)

// StepResult captures the outcome of one step.
type StepResult struct {
	StepName string
	Status   StepStatus
	Output   string
	Error    error
}

// TaskResult captures the outcome of one task.
type TaskResult struct {
	TaskName string
	Steps    []*StepResult
	Error    error
}

// RunResult is the full outcome of a file execution.
type RunResult struct {
	Tasks []*TaskResult
	Error error
}

// ── File ───────────────────────────────────────────────────────────────────

// File is the parsed result of a Loom workflow file.
// The file IS the workflow — no wrapper.
type File struct {
	// Defined agents, scopes, policies, rules (available for use())
	Agents    map[string]*AgentDef
	Scopes    map[string]*Scope
	Policies  map[string]*Policy
	Rules     map[string]*Rule
	Workspaces map[string]*WorkspaceDef
	Scripts   []*ScriptDef

	// Memory config (from memory() call)
	Memory *MemoryConfig

	// Active use config (from use() call — last call wins)
	Use *UseConfig

	// Ordered execution sequence: tasks interleaved with checkpoints
	Sequence []SequenceItem
}

// SequenceItemKind identifies what's in a SequenceItem.
type SequenceItemKind string

const (
	SeqTask       SequenceItemKind = "task"
	SeqCheckpoint SequenceItemKind = "checkpoint"
	SeqClear      SequenceItemKind = "clear"
	SeqClean      SequenceItemKind = "clean"
	SeqFinish     SequenceItemKind = "finish"
)

// SequenceItem is one entry in the file's execution sequence.
type SequenceItem struct {
	Kind       SequenceItemKind
	Task       *Task
	Checkpoint *CheckpointDef
	Clean      *CleanConfig
}

// CleanConfig is the config from clean() in the DSL.
type CleanConfig struct {
	Ignore     []string // "ignore_memory" | "ignore_artifacts" | "ignore_worktrees"
	NewSession bool
	SessionName string
}
