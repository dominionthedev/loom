// Package capability manages runtime operational primitives.
// Workflows declare which capabilities they use. The Guard enforces access.
// Nothing executes outside declared scope + step access.
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

// Input is the key-value params passed to a capability.
type Input map[string]string

// Result is what a capability returns.
type Result struct {
	Output string
	Error  error
}

// Contract describes a capability's operational behaviour.
// The Guard reads this to enforce policies.
type Contract struct {
	Effects []string // e.g. "filesystem.read", "filesystem.modify", "process.spawn"
	Uses    []string // composed capabilities this one depends on
}

// Contracted is implemented by capabilities that declare a contract.
type Contracted interface {
	Contract() Contract
}

// Capability is a named runtime operation.
type Capability interface {
	Name() string
	Description() string
	Execute(ctx context.Context, input Input) Result
}

// Registry holds all registered capabilities.
type Registry struct {
	mu   sync.RWMutex
	caps map[string]Capability
}

// New returns a Registry pre-loaded with all built-in capabilities.
func New() *Registry {
	r := &Registry{caps: make(map[string]Capability)}
	// Filesystem
	r.Register(&fsRead{})
	r.Register(&fsWrite{})
	r.Register(&fsEdit{})
	r.Register(&fsGlob{})
	// Process
	r.Register(&procExecute{})
	r.Register(&procBackground{})
	r.Register(&procWatch{})
	// Web
	r.Register(&webSearch{})
	r.Register(&webFetch{})
	// Model
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

// Execute runs a named capability.
func (r *Registry) Execute(ctx context.Context, name string, input Input) Result {
	c := r.Get(name)
	if c == nil {
		return Result{Error: fmt.Errorf("capability %q not registered", name)}
	}
	return c.Execute(ctx, input)
}

// Describe returns formatted descriptions for the given capability names.
// Injected into the agent's system prompt.
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

// ── filesystem.read ───────────────────────────────────────────────────────

type fsRead struct{}

func (f *fsRead) Name() string        { return "filesystem.read" }
func (f *fsRead) Description() string { return "Read a file. params: path" }
func (f *fsRead) Contract() Contract  { return Contract{Effects: []string{"filesystem.read"}} }
func (f *fsRead) Execute(_ context.Context, input Input) Result {
	path := input["path"]
	if path == "" {
		return Result{Error: fmt.Errorf("filesystem.read: missing param 'path'")}
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
func (f *fsWrite) Description() string { return "Write content to a file. params: path, content" }
func (f *fsWrite) Contract() Contract  { return Contract{Effects: []string{"filesystem.modify"}} }
func (f *fsWrite) Execute(_ context.Context, input Input) Result {
	path, content := input["path"], input["content"]
	if path == "" {
		return Result{Error: fmt.Errorf("filesystem.write: missing param 'path'")}
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
func (f *fsEdit) Description() string { return "Edit a file in-place (find + replace). params: path, find, replace" }
func (f *fsEdit) Contract() Contract  { return Contract{Effects: []string{"filesystem.modify"}} }
func (f *fsEdit) Execute(_ context.Context, input Input) Result {
	path := input["path"]
	find := input["find"]
	replace := input["replace"]
	if path == "" || find == "" {
		return Result{Error: fmt.Errorf("filesystem.edit: missing params 'path' and/or 'find'")}
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
func (f *fsGlob) Description() string { return "Find files matching a glob pattern. params: pattern" }
func (f *fsGlob) Contract() Contract  { return Contract{Effects: []string{"filesystem.read"}} }
func (f *fsGlob) Execute(_ context.Context, input Input) Result {
	pattern := input["pattern"]
	if pattern == "" {
		return Result{Error: fmt.Errorf("filesystem.glob: missing param 'pattern'")}
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
func (p *procExecute) Description() string { return "Run a shell command. params: cmd" }
func (p *procExecute) Contract() Contract  { return Contract{Effects: []string{"process.spawn"}} }
func (p *procExecute) Execute(ctx context.Context, input Input) Result {
	cmd := input["cmd"]
	if cmd == "" {
		return Result{Error: fmt.Errorf("process.execute: missing param 'cmd'")}
	}
	out, err := exec.CommandContext(ctx, "sh", "-c", cmd).CombinedOutput()
	return Result{Output: string(out), Error: err}
}

// ── process.background ────────────────────────────────────────────────────

type procBackground struct{}

func (p *procBackground) Name() string        { return "process.background" }
func (p *procBackground) Description() string { return "Start a background process. params: cmd" }
func (p *procBackground) Contract() Contract  { return Contract{Effects: []string{"process.spawn"}} }
func (p *procBackground) Execute(_ context.Context, input Input) Result {
	cmd := input["cmd"]
	if cmd == "" {
		return Result{Error: fmt.Errorf("process.background: missing param 'cmd'")}
	}
	c := exec.Command("sh", "-c", cmd)
	if err := c.Start(); err != nil {
		return Result{Error: fmt.Errorf("process.background: %w", err)}
	}
	return Result{Output: fmt.Sprintf("started background process (pid %d)", c.Process.Pid)}
}

// ── process.watch ─────────────────────────────────────────────────────────

type procWatch struct{}

func (p *procWatch) Name() string        { return "process.watch" }
func (p *procWatch) Description() string { return "Watch a path pattern for changes. params: pattern" }
func (p *procWatch) Contract() Contract  { return Contract{Effects: []string{"filesystem.read"}} }
func (p *procWatch) Execute(_ context.Context, input Input) Result {
	// V0.1: stub — returns the pattern that would be watched.
	// Full implementation (fsnotify) in v0.3.
	pattern := input["pattern"]
	return Result{Output: fmt.Sprintf("watching: %s (stub)", pattern)}
}

// ── web.search ────────────────────────────────────────────────────────────

type webSearch struct{}

func (w *webSearch) Name() string        { return "web.search" }
func (w *webSearch) Description() string { return "Search the web. params: query" }
func (w *webSearch) Contract() Contract  { return Contract{Effects: []string{"web.read"}} }
func (w *webSearch) Execute(_ context.Context, input Input) Result {
	// V0.1: stub — full implementation in v0.3.
	query := input["query"]
	return Result{Output: fmt.Sprintf("web.search stub: %q", query)}
}

// ── web.fetch ─────────────────────────────────────────────────────────────

type webFetch struct{}

func (w *webFetch) Name() string        { return "web.fetch" }
func (w *webFetch) Description() string { return "Fetch a URL. params: url" }
func (w *webFetch) Contract() Contract  { return Contract{Effects: []string{"web.read"}} }
func (w *webFetch) Execute(_ context.Context, input Input) Result {
	// V0.1: stub — full implementation in v0.3.
	url := input["url"]
	return Result{Output: fmt.Sprintf("web.fetch stub: %q", url)}
}

// ── model.think ───────────────────────────────────────────────────────────

type modelThink struct{}

func (m *modelThink) Name() string        { return "model.think" }
func (m *modelThink) Description() string { return "Enable extended thinking mode on the model. params: level" }
func (m *modelThink) Contract() Contract  { return Contract{Effects: []string{}} }
func (m *modelThink) Execute(_ context.Context, input Input) Result {
	level := input["level"]
	if level == "" {
		level = "medium"
	}
	return Result{Output: fmt.Sprintf("think level: %s", level)}
}
