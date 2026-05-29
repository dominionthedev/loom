// Package guard enforces every operational boundary in Loom.
// It sits between the orchestrator and every capability execution.
// Nothing runs that isn't permitted by scope + policy + step declaration.
package guard

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dominionthedev/loom/internal/capability"
	"github.com/dominionthedev/loom/internal/workflow"
)

// Violation is a Guard enforcement failure.
type Violation struct {
	Rule    string // which rule fired
	Message string
}

func (v *Violation) Error() string {
	return fmt.Sprintf("guard [%s]: %s", v.Rule, v.Message)
}

// Guard enforces Loom's operational boundaries.
type Guard struct {
	caps *capability.Registry
}

// New returns a Guard wired to the capability registry.
func New(caps *capability.Registry) *Guard {
	return &Guard{caps: caps}
}

// Check validates a capability call against:
//  1. Scope — is the capability declared?
//  2. Step access — is the capability called in this step?
//  3. Path — does the path match include/exclude patterns?
//  4. Policy — does the call match any deny/allow/limit rule?
//
// Returns nil if execution is permitted.
func (g *Guard) Check(
	scope *workflow.Scope,
	policies []*workflow.Policy,
	stepCaps []workflow.CapCall,
	capName string,
	input capability.Input,
) *Violation {

	// ── 1. Scope enforcement ──────────────────────────────────────────
	if !scope.AllowsCapability(capName) {
		return &Violation{
			Rule:    "scope.capability",
			Message: fmt.Sprintf("capability %q is not declared in scope %q", capName, scope.Name),
		}
	}

	// ── 2. Step access enforcement ────────────────────────────────────
	if !stepAllows(stepCaps, capName) {
		return &Violation{
			Rule:    "step.capability",
			Message: fmt.Sprintf("capability %q is not declared in this step", capName),
		}
	}

	// ── 3. Path enforcement (filesystem capabilities) ─────────────────
	if isFilesystemCap(capName) {
		if path, ok := input["path"]; ok && path != "" {
			if v := g.checkPath(scope, capName, path); v != nil {
				return v
			}
		}
	}

	// ── 4. Policy enforcement ─────────────────────────────────────────
	cap := g.caps.Get(capName)
	var contract capability.Contract
	if cap != nil {
		if contracted, ok := cap.(capability.Contracted); ok {
			contract = contracted.Contract()
		}
	}

	for _, p := range policies {
		if v := checkPolicy(p, capName, contract, input); v != nil {
			return v
		}
	}

	return nil
}

// checkPath enforces include/exclude glob rules on a filesystem path.
func (g *Guard) checkPath(scope *workflow.Scope, capName, path string) *Violation {
	path = filepath.ToSlash(filepath.Clean(path))

	// Excludes win over includes.
	for _, pattern := range scope.Exclude {
		if globMatch(pattern, path) {
			return &Violation{
				Rule:    "scope.path.exclude",
				Message: fmt.Sprintf("%s: path %q is excluded by pattern %q", capName, path, pattern),
			}
		}
	}

	// If includes declared, path must match at least one.
	if len(scope.Include) > 0 {
		for _, pattern := range scope.Include {
			if globMatch(pattern, path) {
				return nil
			}
		}
		return &Violation{
			Rule:    "scope.path.include",
			Message: fmt.Sprintf("%s: path %q does not match any include pattern in scope %q", capName, path, scope.Name),
		}
	}

	return nil
}

// checkPolicy evaluates one policy rule against a capability call.
func checkPolicy(
	p *workflow.Policy,
	capName string,
	contract capability.Contract,
	input capability.Input,
) *Violation {

	// Only apply this policy if the target matches the capability.
	if p.Target != capName && p.Target != "" {
		return nil
	}

	switch p.Kind {

	case workflow.PolicyDeny:
		// Check against match patterns for process capabilities (command matching).
		if isProcessCap(capName) {
			cmd := input["cmd"]
			for _, pattern := range p.Match {
				if globMatch(pattern, cmd) {
					return &Violation{
						Rule:    "policy.deny",
						Message: fmt.Sprintf("policy %q: capability %q with command %q is denied by pattern %q", p.Name, capName, cmd, pattern),
					}
				}
			}
		}
		// For non-process caps with no match patterns: deny the whole capability.
		if !isProcessCap(capName) && len(p.Match) == 0 {
			return &Violation{
				Rule:    "policy.deny",
				Message: fmt.Sprintf("policy %q: capability %q is denied", p.Name, capName),
			}
		}

	case workflow.PolicyLimit:
		// Allow only if the path/command matches one of the patterns.
		if isProcessCap(capName) {
			cmd := input["cmd"]
			for _, pattern := range p.Match {
				if globMatch(pattern, cmd) {
					return nil // permitted
				}
			}
			return &Violation{
				Rule:    "policy.limit",
				Message: fmt.Sprintf("policy %q: command %q does not match any allowed pattern", p.Name, cmd),
			}
		}
		if isFilesystemCap(capName) {
			path := filepath.ToSlash(input["path"])
			for _, pattern := range p.Match {
				if globMatch(pattern, path) {
					return nil
				}
			}
			return &Violation{
				Rule:    "policy.limit",
				Message: fmt.Sprintf("policy %q: path %q does not match any allowed pattern", p.Name, path),
			}
		}

	case workflow.PolicyAllow:
		// Explicit allow — no violation.
		return nil
	}

	return nil
}

// stepAllows checks if a capability was declared in the step's cap calls.
func stepAllows(calls []workflow.CapCall, capName string) bool {
	for _, c := range calls {
		if c.All {
			return true // all_capabilities()
		}
		if c.Name == capName {
			return true
		}
	}
	return false
}

// ── Helpers ────────────────────────────────────────────────────────────────

func isFilesystemCap(name string) bool {
	return strings.HasPrefix(name, "filesystem.")
}

func isProcessCap(name string) bool {
	return strings.HasPrefix(name, "process.")
}

// globMatch matches a glob pattern against a target string.
// Supports ** for multi-segment wildcards.
func globMatch(pattern, target string) bool {
	if pattern == target {
		return true
	}
	if strings.Contains(pattern, "**") {
		return doubleStarMatch(pattern, target)
	}
	matched, _ := filepath.Match(pattern, target)
	return matched
}

func doubleStarMatch(pattern, target string) bool {
	parts := strings.SplitN(pattern, "**", 2)
	prefix, suffix := parts[0], strings.TrimPrefix(parts[1], "/")

	if prefix != "" {
		if !strings.HasPrefix(target, prefix) {
			return false
		}
		target = target[len(prefix):]
	}

	if suffix == "" {
		return true
	}

	if strings.HasSuffix(target, suffix) {
		return true
	}

	segments := strings.Split(target, "/")
	for i := range segments {
		candidate := strings.Join(segments[i:], "/")
		if matched, _ := filepath.Match(suffix, candidate); matched {
			return true
		}
	}
	return false
}
