// Package capability manages runtime operational primitives.
// Each capability declares a schema — which fields it needs and where they come from.
// Args from the Lua file constrain specific fields. The agent provides the rest.
// The Guard enforces scope and policy; capabilities handle their own field resolution.
package capability

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
)

// ── Field schema ───────────────────────────────────────────────────────────

// FieldSource declares where a capability field's value comes from.
type FieldSource string

const (
	// SourceArg — value set by Lua args. Agent never provides this field.
	SourceArg FieldSource = "arg"
	// SourceContext — always from the agent. Args cannot set this.
	SourceContext FieldSource = "context"
	// SourceEither — from args if given (cancels agent's choice), else agent provides it.
	SourceEither FieldSource = "either"
)

// Field is one input field of a capability.
type Field struct {
	Name        string
	Source      FieldSource
	Description string // shown to the agent when it must provide this field
}

// Schema is a capability's full input declaration.
type Schema struct {
	Fields []Field
}

// EffectiveSchema returns the schema the agent sees after args are applied.
// Fields covered by args are removed — the agent doesn't need to provide them.
func (s Schema) EffectiveSchema(args map[string]string) Schema {
	var remaining []Field
	for _, f := range s.Fields {
		if f.Source == SourceContext {
			remaining = append(remaining, f)
			continue
		}
		if _, covered := args[f.Name]; !covered {
			remaining = append(remaining, f)
		}
		// If args covers this field: omit from effective schema.
	}
	return Schema{Fields: remaining}
}

// AgentDescription returns a human-readable description for the agent.
// Shows the capability name, what it does, and what fields the agent must provide.
func (s Schema) AgentDescription(name, description string, args map[string]string) string {
	effective := s.EffectiveSchema(args)
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("%s: %s", name, description))

	// Show fixed args so the agent knows what's locked.
	if len(args) > 0 {
		var fixed []string
		for k, v := range args {
			fixed = append(fixed, fmt.Sprintf("%s=%q", k, v))
		}
		sb.WriteString(fmt.Sprintf(" [fixed: %s]", strings.Join(fixed, ", ")))
	}

	// Show fields the agent must provide.
	if len(effective.Fields) > 0 {
		var needed []string
		for _, f := range effective.Fields {
			needed = append(needed, fmt.Sprintf("%s (%s)", f.Name, f.Description))
		}
		sb.WriteString(fmt.Sprintf(" [agent provides: %s]", strings.Join(needed, ", ")))
	}

	return sb.String()
}

// ── Input / Result ─────────────────────────────────────────────────────────

// Input is the resolved key-value params for a capability execution.
// Built by merging args (static) with context inputs (from agent).
type Input map[string]string

// Merge returns a new Input combining base (args) with override (agent context).
// Args take priority — they cannot be overridden by the agent.
func (base Input) Merge(agentInput Input) Input {
	result := make(Input, len(base)+len(agentInput))
	for k, v := range agentInput {
		result[k] = v
	}
	// Args always win.
	for k, v := range base {
		result[k] = v
	}
	return result
}

// Result is what a capability returns.
type Result struct {
	Output string
	Error  error
}

// ── Contract ───────────────────────────────────────────────────────────────

// Contract describes a capability's operational behaviour.
type Contract struct {
	Effects []string // e.g. "filesystem.read", "filesystem.modify", "process.spawn"
	Uses    []string // composed capabilities this one depends on
}

// Contracted is implemented by capabilities that declare a contract.
type Contracted interface {
	Contract() Contract
}

// Schematic is implemented by capabilities that declare a field schema.
type Schematic interface {
	Schema() Schema
}

// ── Capability interface ───────────────────────────────────────────────────

// Capability is a named runtime operation.
type Capability interface {
	Name() string
	Description() string
	Execute(ctx context.Context, input Input) Result
}

// ── Registry ──────────────────────────────────────────────────────────────

// Registry holds all registered capabilities.
type Registry struct {
	mu   sync.RWMutex
	caps map[string]Capability
}

// New returns a Registry pre-loaded with all built-in capabilities.
func New() *Registry {
	r := &Registry{caps: make(map[string]Capability)}
	r.Register(&fsRead{})
	r.Register(&fsWrite{})
	r.Register(&fsEdit{})
	r.Register(&fsGlob{})
	r.Register(&procExecute{})
	r.Register(&procBackground{})
	r.Register(&procWatch{})
	r.Register(&webSearch{})
	r.Register(&webFetch{})
	r.Register(&modelThink{})
	return r
}

// Register adds a capability.
func (r *Registry) Register(c Capability) {
	r.mu.Lock()
	r.caps[c.Name()] = c
	r.mu.Unlock()
}

// Get returns a capability by name. Returns nil if not found.
func (r *Registry) Get(name string) Capability {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.caps[name]
}

// Execute runs a named capability with the given input.
func (r *Registry) Execute(ctx context.Context, name string, input Input) Result {
	c := r.Get(name)
	if c == nil {
		return Result{Error: fmt.Errorf("capability %q not registered", name)}
	}
	return c.Execute(ctx, input)
}

// Names returns all registered capability names.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.caps))
	for name := range r.caps {
		names = append(names, name)
	}
	return names
}

// DescribeForAgent returns a formatted tool list for the agent prompt.
// capNames is the list of capabilities available in this step.
// args is a map of capName → static args from the Lua file.
func (r *Registry) DescribeForAgent(capNames []string, args map[string]map[string]string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var sb strings.Builder
	for _, name := range capNames {
		c, ok := r.caps[name]
		if !ok {
			continue
		}
		capArgs := args[name]
		if s, ok := c.(Schematic); ok {
			schema := s.Schema()
			sb.WriteString("- " + schema.AgentDescription(c.Name(), c.Description(), capArgs) + "\n")
		} else {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", c.Name(), c.Description()))
		}
	}
	return sb.String()
}

// Describe returns basic descriptions for scope context (non-step agent prompt).
func (r *Registry) Describe(names []string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var sb strings.Builder
	for _, name := range names {
		if c, ok := r.caps[name]; ok {
			sb.WriteString(fmt.Sprintf("- %s: %s\n", c.Name(), c.Description()))
		}
	}
	return sb.String()
}

// ── filesystem.read ───────────────────────────────────────────────────────

type fsRead struct{}

func (f *fsRead) Name() string        { return "filesystem.read" }
func (f *fsRead) Description() string { return "Read a file's contents" }
func (f *fsRead) Contract() Contract  { return Contract{Effects: []string{"filesystem.read"}} }
func (f *fsRead) Schema() Schema {
	return Schema{Fields: []Field{
		{Name: "path", Source: SourceEither, Description: "file path to read"},
	}}
}
func (f *fsRead) Execute(_ context.Context, input Input) Result {
	path := input["path"]
	if path == "" {
		return Result{Error: fmt.Errorf("filesystem.read: missing 'path'")}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{Error: fmt.Errorf("filesystem.read: %w", err)}
	}
	return Result{Output: string(data)}
}

// ── filesystem.write ──────────────────────────────────────────────────────

type fsWrite struct{}

func (f *fsWrite) Name() string        { return "filesystem.write" }
func (f *fsWrite) Description() string { return "Write content to a file" }
func (f *fsWrite) Contract() Contract  { return Contract{Effects: []string{"filesystem.modify"}} }
func (f *fsWrite) Schema() Schema {
	return Schema{Fields: []Field{
		{Name: "path", Source: SourceEither, Description: "file path to write"},
		{Name: "content", Source: SourceContext, Description: "content to write"},
	}}
}
func (f *fsWrite) Execute(_ context.Context, input Input) Result {
	path, content := input["path"], input["content"]
	if path == "" {
		return Result{Error: fmt.Errorf("filesystem.write: missing 'path'")}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return Result{Error: fmt.Errorf("filesystem.write: mkdir: %w", err)}
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return Result{Error: fmt.Errorf("filesystem.write: %w", err)}
	}
	return Result{Output: fmt.Sprintf("wrote %d bytes → %s", len(content), path)}
}

// ── filesystem.edit ───────────────────────────────────────────────────────

type fsEdit struct{}

func (f *fsEdit) Name() string        { return "filesystem.edit" }
func (f *fsEdit) Description() string { return "Edit a file in-place" }
func (f *fsEdit) Contract() Contract  { return Contract{Effects: []string{"filesystem.modify"}} }
func (f *fsEdit) Schema() Schema {
	return Schema{Fields: []Field{
		{Name: "path", Source: SourceContext, Description: "file path to edit"},
		{Name: "find", Source: SourceContext, Description: "exact text to find"},
		{Name: "replace", Source: SourceContext, Description: "replacement text"},
	}}
}
func (f *fsEdit) Execute(_ context.Context, input Input) Result {
	path, find, replace := input["path"], input["find"], input["replace"]
	if path == "" || find == "" {
		return Result{Error: fmt.Errorf("filesystem.edit: missing 'path' or 'find'")}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Result{Error: fmt.Errorf("filesystem.edit: read: %w", err)}
	}
	updated := strings.ReplaceAll(string(data), find, replace)
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		return Result{Error: fmt.Errorf("filesystem.edit: write: %w", err)}
	}
	return Result{Output: fmt.Sprintf("edited %s", path)}
}

// ── filesystem.glob ───────────────────────────────────────────────────────

type fsGlob struct{}

func (f *fsGlob) Name() string        { return "filesystem.glob" }
func (f *fsGlob) Description() string { return "Find files matching a glob pattern" }
func (f *fsGlob) Contract() Contract  { return Contract{Effects: []string{"filesystem.read"}} }
func (f *fsGlob) Schema() Schema {
	return Schema{Fields: []Field{
		{Name: "pattern", Source: SourceEither, Description: "glob pattern e.g. **/*.go"},
	}}
}
func (f *fsGlob) Execute(_ context.Context, input Input) Result {
	pattern := input["pattern"]
	if pattern == "" {
		return Result{Error: fmt.Errorf("filesystem.glob: missing 'pattern'")}
	}
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return Result{Error: fmt.Errorf("filesystem.glob: %w", err)}
	}
	return Result{Output: strings.Join(matches, "\n")}
}

// ── process.execute ───────────────────────────────────────────────────────

type procExecute struct{}

func (p *procExecute) Name() string        { return "process.execute" }
func (p *procExecute) Description() string { return "Run a shell command" }
func (p *procExecute) Contract() Contract  { return Contract{Effects: []string{"process.spawn"}} }
func (p *procExecute) Schema() Schema {
	return Schema{Fields: []Field{
		{Name: "cmd", Source: SourceEither, Description: "shell command to run"},
	}}
}
func (p *procExecute) Execute(ctx context.Context, input Input) Result {
	cmd := input["cmd"]
	if cmd == "" {
		return Result{Error: fmt.Errorf("process.execute: missing 'cmd'")}
	}
	out, err := exec.CommandContext(ctx, "sh", "-c", cmd).CombinedOutput()
	return Result{Output: string(out), Error: err}
}

// ── process.background ────────────────────────────────────────────────────

type procBackground struct{}

func (p *procBackground) Name() string        { return "process.background" }
func (p *procBackground) Description() string { return "Start a background process" }
func (p *procBackground) Contract() Contract  { return Contract{Effects: []string{"process.spawn"}} }
func (p *procBackground) Schema() Schema {
	return Schema{Fields: []Field{
		{Name: "cmd", Source: SourceEither, Description: "command to run in background"},
	}}
}
func (p *procBackground) Execute(_ context.Context, input Input) Result {
	cmd := input["cmd"]
	if cmd == "" {
		return Result{Error: fmt.Errorf("process.background: missing 'cmd'")}
	}
	c := exec.Command("sh", "-c", cmd)
	if err := c.Start(); err != nil {
		return Result{Error: fmt.Errorf("process.background: %w", err)}
	}
	return Result{Output: fmt.Sprintf("background pid %d", c.Process.Pid)}
}

// ── process.watch ─────────────────────────────────────────────────────────

type procWatch struct{}

func (p *procWatch) Name() string        { return "process.watch" }
func (p *procWatch) Description() string { return "Watch a path for changes" }
func (p *procWatch) Contract() Contract  { return Contract{Effects: []string{"filesystem.read"}} }
func (p *procWatch) Schema() Schema {
	return Schema{Fields: []Field{
		{Name: "pattern", Source: SourceEither, Description: "path pattern to watch"},
	}}
}
func (p *procWatch) Execute(_ context.Context, input Input) Result {
	return Result{Output: fmt.Sprintf("watching: %s (v0.3)", input["pattern"])}
}

// ── web.search ────────────────────────────────────────────────────────────

type webSearch struct{}

func (w *webSearch) Name() string        { return "web.search" }
func (w *webSearch) Description() string { return "Search the web" }
func (w *webSearch) Contract() Contract  { return Contract{Effects: []string{"web.read"}} }
func (w *webSearch) Schema() Schema {
	return Schema{Fields: []Field{
		{Name: "query", Source: SourceContext, Description: "search query"},
	}}
}
func (w *webSearch) Execute(_ context.Context, input Input) Result {
	return Result{Output: fmt.Sprintf("web.search: %q (v0.3)", input["query"])}
}

// ── web.fetch ─────────────────────────────────────────────────────────────

type webFetch struct{}

func (w *webFetch) Name() string        { return "web.fetch" }
func (w *webFetch) Description() string { return "Fetch a URL" }
func (w *webFetch) Contract() Contract  { return Contract{Effects: []string{"web.read"}} }
func (w *webFetch) Schema() Schema {
	return Schema{Fields: []Field{
		{Name: "url", Source: SourceContext, Description: "URL to fetch"},
	}}
}
func (w *webFetch) Execute(_ context.Context, input Input) Result {
	return Result{Output: fmt.Sprintf("web.fetch: %q (v0.3)", input["url"])}
}

// ── model.think ───────────────────────────────────────────────────────────

type modelThink struct{}

func (m *modelThink) Name() string        { return "model.think" }
func (m *modelThink) Description() string { return "Enable extended thinking mode" }
func (m *modelThink) Contract() Contract  { return Contract{} }
func (m *modelThink) Schema() Schema {
	return Schema{Fields: []Field{
		{Name: "level", Source: SourceArg, Description: "thinking level"},
	}}
}
func (m *modelThink) Execute(_ context.Context, input Input) Result {
	return Result{Output: fmt.Sprintf("think: %s", input["level"])}
}
