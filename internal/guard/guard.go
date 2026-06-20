// Package guard enforces every operational boundary in Loom.
// Every capability call — from the agent loop and from the fast path —
// passes through Guard.Check before it executes.
package guard

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/dominionthedev/loom/internal/capability"
	"github.com/dominionthedev/loom/internal/globutil"
	"github.com/dominionthedev/loom/internal/workflow"
)

// Violation is a Guard enforcement failure.
type Violation struct {
	Rule    string
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
//  1. Scope        — is the capability declared at all?
//  2. Step          — is the capability declared in this step?
//  3. Path          — does the path match include/exclude globs?
//  4. Command paths — does the shell command reference an excluded path?
//  5. Policy        — deny/allow/limit by capability + match pattern.
//
// allowedCaps is the step's declared capability set (all_capabilities()
// already expanded to concrete names by the caller).
//
// artifactsCall is true for read(artifacts)/write(artifacts) calls — these
// operate inside .loom/artifacts/, which is Loom's own storage, not part
// of the project source the scope's include/exclude restricts. Step 3 is
// skipped for these; the caller is responsible for resolving the path
// into the artifacts directory and verifying it doesn't escape it.
//
// Returns nil if execution is permitted.
func (g *Guard) Check(
	scope *workflow.Scope,
	policies []*workflow.Policy,
	allowedCaps []string,
	capName string,
	input capability.Input,
	artifactsCall bool,
) *Violation {

	// 1. Scope.
	if !scope.AllowsCapability(capName) {
		return &Violation{
			Rule:    "scope.capability",
			Message: fmt.Sprintf("capability %q is not declared in scope %q", capName, scope.Name),
		}
	}

	// 2. Step.
	if !contains(allowedCaps, capName) {
		return &Violation{
			Rule:    "step.capability",
			Message: fmt.Sprintf("capability %q is not declared in this step — add it to the step or scope if the agent needs it", capName),
		}
	}

	// 3. Path — filesystem capabilities respect include/exclude globs.
	// Skipped for artifacts-domain calls (see doc comment above).
	if !artifactsCall && isFilesystemCap(capName) {
		if path, ok := input["path"]; ok && path != "" {
			if v := g.checkPath(scope, capName, path); v != nil {
				return v
			}
		}
	}

	// 4. Command path scan — best-effort, process capabilities only.
	if isProcessCap(capName) {
		if cmd, ok := input["cmd"]; ok && cmd != "" {
			if v := g.checkCommandPaths(scope, capName, cmd); v != nil {
				return v
			}
		}
	}

	// 5. Policy.
	var contract capability.Contract
	if c := g.caps.Get(capName); c != nil {
		if contracted, ok := c.(capability.Contracted); ok {
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

	for _, pattern := range scope.Exclude {
		if globutil.Match(pattern, path) {
			return &Violation{
				Rule:    "scope.path.exclude",
				Message: fmt.Sprintf("%s: path %q is excluded by pattern %q", capName, path, pattern),
			}
		}
	}

	if len(scope.Include) > 0 {
		for _, pattern := range scope.Include {
			if globutil.Match(pattern, path) {
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

// checkCommandPaths scans a shell command string for path-like tokens and
// blocks any that match an EXCLUDED pattern. It deliberately does NOT
// require an include match — command arguments are often flags or globs
// ("./...", "-v", "**/*_test.go"), not literal files, and requiring strict
// inclusion would produce constant false positives (blocking "go test ./...").
//
// This is a heuristic, not a sandbox. It catches the obvious case — an
// agent running `cat .env` or `rm -rf secrets/` — but it can be bypassed
// by quoting, piping, or encoding. Real isolation requires a runbox-backed
// execution environment (future).
func (g *Guard) checkCommandPaths(scope *workflow.Scope, capName, cmd string) *Violation {
	for _, token := range extractPathTokens(cmd) {
		clean := filepath.ToSlash(filepath.Clean(token))
		for _, pattern := range scope.Exclude {
			if globutil.Match(pattern, clean) {
				return &Violation{
					Rule:    "scope.command.exclude",
					Message: fmt.Sprintf("%s: command references excluded path %q (pattern %q)", capName, token, pattern),
				}
			}
		}
	}
	return nil
}

// extractPathTokens does a best-effort scan for path-like tokens in a
// shell command: whitespace-split, skip flags, keep tokens containing
// '/' or '.'.
func extractPathTokens(cmd string) []string {
	fields := strings.Fields(cmd)
	var tokens []string
	for _, f := range fields {
		f = strings.Trim(f, `"'`)
		if f == "" || strings.HasPrefix(f, "-") {
			continue
		}
		if strings.Contains(f, "/") || strings.Contains(f, ".") {
			tokens = append(tokens, f)
		}
	}
	return tokens
}

// checkPolicy evaluates one policy rule against a capability call.
func checkPolicy(
	p *workflow.Policy,
	capName string,
	contract capability.Contract,
	input capability.Input,
) *Violation {

	if p.Target != capName && p.Target != "" {
		return nil
	}

	switch p.Kind {

	case workflow.PolicyDeny:
		if isProcessCap(capName) {
			cmd := input["cmd"]
			for _, pattern := range p.Match {
				if globutil.Match(pattern, cmd) {
					return &Violation{
						Rule:    "policy.deny",
						Message: fmt.Sprintf("policy %q: %q matches denied pattern %q", p.Name, cmd, pattern),
					}
				}
			}
		}
		if !isProcessCap(capName) && len(p.Match) == 0 {
			return &Violation{
				Rule:    "policy.deny",
				Message: fmt.Sprintf("policy %q: capability %q is denied", p.Name, capName),
			}
		}

	case workflow.PolicyLimit:
		if isProcessCap(capName) {
			cmd := input["cmd"]
			for _, pattern := range p.Match {
				if globutil.Match(pattern, cmd) {
					return nil
				}
			}
			return &Violation{
				Rule:    "policy.limit",
				Message: fmt.Sprintf("policy %q: %q does not match any allowed pattern", p.Name, cmd),
			}
		}
		if isFilesystemCap(capName) {
			path := filepath.ToSlash(input["path"])
			for _, pattern := range p.Match {
				if globutil.Match(pattern, path) {
					return nil
				}
			}
			return &Violation{
				Rule:    "policy.limit",
				Message: fmt.Sprintf("policy %q: path %q does not match any allowed pattern", p.Name, path),
			}
		}

	case workflow.PolicyAllow:
		return nil
	}

	return nil
}

// ── Helpers ────────────────────────────────────────────────────────────────

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func isFilesystemCap(name string) bool { return strings.HasPrefix(name, "filesystem.") }
func isProcessCap(name string) bool    { return strings.HasPrefix(name, "process.") }
