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
	PolicyDeny  PolicyKind = "deny"
	PolicyAllow PolicyKind = "allow"
	PolicyLimit PolicyKind = "limit"
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
// Rules shape how the agent reasons — not just what it can execute.
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
// Scripts are files — never anonymous inline code.
// They run outside the agent context, executed by the runtime.
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
	GuardLow       GuardLevel = "low"       // allow/deny menu
	GuardImportant GuardLevel = "important" // rule enforcement + TouchID/password
	GuardCritical  GuardLevel = "critical"  // discipline agent + TouchID + notification + trace
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

// StepKind classifies the primary operation of a step.
type StepKind string

const (
	// StepReason: agent reasons with a prompt. Output → context.
	// The agent can write if write() is declared in the step's CapCalls.
	// reason() is the primary interface to the agent.
	StepReason StepKind = "reason"

	// StepPlan: agent produces a structured plan. Output → artifact.
	// Always runs at high thinking level. think() has no effect inside plan steps.
	// Guard level escalates automatically.
	StepPlan StepKind = "plan"
)

// CapCall is a capability invocation declared inside a step.
// Only capabilities declared in CapCalls are accessible — even if in scope.
type CapCall struct {
	Name   string   // capability name e.g. "filesystem.read"
	Args   []string // explicit args (empty = open, policy-filtered)
	All    bool     // all_capabilities() — full scope access
	Custom bool     // capability("name") — user-defined

	// ArtifactsScope is true for read(artifacts) / write(artifacts).
	// This is NOT a literal path — it restricts the capability's domain
	// to .loom/artifacts/. The agent still chooses which file by name;
	// the runtime resolves it relative to the artifacts dir and exempts
	// it from the project scope's include/exclude path check (Loom's own
	// storage isn't part of the project source the scope restricts).
	ArtifactsScope bool
}

// ArtifactRef is an artifact operation inside a step.
// Create=false: reference — load into agent context (before reasoning).
// Create=true:  create   — write output as a named artifact (after write/plan).
type ArtifactRef struct {
	Name   string
	Create bool
}

// Step is a single configured execution unit inside a task.
//
// Convention (not enforced, but correct):
//
//	depends_on()     — first: import context + enforce DAG
//	artifacts(ref)   — before reasoning: load artifact into context
//	think()          — set thinking mode (no effect on plan steps)
//	reason()/plan()  — reasoning with prompt
//	read()/write()/execute()/glob()/...  — capability access
//	export()         — mark context for downstream steps
//	artifacts(create) — after write/plan: save as named artifact
//	guard()          — guard level for this step
//	review()         — pause for manual approval
type Step struct {
	Name string
	Kind StepKind // reason | plan | "" (capability-only step)

	// Context flow
	DependsOn []string // step names — enforces DAG + imports exported context
	Export    bool     // make this step's context available downstream
	          //         Requires memory() to be initialized for cross-task sharing.

	// Reasoning
	Prompt     string
	ThinkLevel string // overrides agent default (no effect on plan steps)

	// Capability access — step only has access to what's declared here
	CapCalls []CapCall

	// Artifacts — ordered: refs before reasoning, creates after
	ArtifactRefs []ArtifactRef

	// Guard + review
	Guard       GuardLevel
	Review      bool
	ReviewAgent string

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
// Each task gets its own agent instance, locked to its use() config.
// Tasks do not share agent history — context flows only via export()/depends_on()
// and memory() when initialized.
type Task struct {
	Name  string
	Use   *UseConfig
	Steps []*Step
}

// ── Checkpoint ─────────────────────────────────────────────────────────────

// CheckpointType classifies what a checkpoint creates.
type CheckpointType string

const (
	// CheckpointWorktree copies the project to .loom/worktrees/<label>/.
	// Both workspace() and checkpoint() can create worktrees.
	// Checkpoint is the best place when you need a snapshot before a risky task.
	CheckpointWorktree CheckpointType = "worktree"

	// CheckpointState snapshots runtime state only (no file copy).
	CheckpointState CheckpointType = "state"
)

// CheckpointDef is a checkpoint declaration in the sequence.
// Checkpoint marks a point in execution where:
//   - state is snapped
//   - worktree may be created
//   - developer can review an artifact
//   - scripts/commands can run
//   - execution can be rolled back to
type CheckpointDef struct {
	Label      string
	Type       CheckpointType
	ReviewFile string // artifact path to present (empty = no review gate)
}

// ── Memory ─────────────────────────────────────────────────────────────────

// MemoryConfig is the memory initialization config from memory() in the DSL.
// memory() must be initialized for export() to share context across tasks.
type MemoryConfig struct {
	Persist  bool
	Location string // "local" (.loom/memory) | "global" (~/.loom/memory)
	Compress bool
}

// ── Runtime layers ─────────────────────────────────────────────────────────

// Loom has three runtime scopes:
//
//	Global  (~/.loom/)       — across projects, no project required.
//	                           Workflows run here before loom init.
//	                           On init, global state transfers to project.
//	Project (./.loom/)       — across workflows/entire project lifetime.
//	                           Cleared only manually.
//	Session (per workflow)   — across tasks in one workflow run.
//	                           Cleared by clean().
//
// clear() cleans task residue only (not checkpoints, artifacts, worktrees).
// clean() resets the session (optionally starting a new named one).
// Global workflows run without a project; when you init loom, threads transfer.

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

// DeniedCapability records a capability the agent attempted but couldn't
// access — either undeclared in scope/step, or blocked by Guard (path,
// command-path heuristic, or policy). Surfaced as a non-blocking, end-of-run
// report so a developer can catch a missing declaration that wasn't
// intentional, rather than the agent silently working around it.
type DeniedCapability struct {
	Step   string
	Tool   string
	Reason string
}

// TaskResult captures the outcome of one task.
type TaskResult struct {
	TaskName string
	Steps    []*StepResult
	Error    error
	Denied   []DeniedCapability
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
	Agents     map[string]*AgentDef
	Scopes     map[string]*Scope
	Policies   map[string]*Policy
	Rules      map[string]*Rule
	Workspaces map[string]*WorkspaceDef
	Scripts    []*ScriptDef
	Memory     *MemoryConfig
	Use        *UseConfig // active use() config — last call wins

	// Ordered execution sequence.
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
// Ignore options: "ignore_memory", "ignore_artifacts", "ignore_worktrees"
type CleanConfig struct {
	Ignore      []string
	NewSession  bool
	SessionName string
}
