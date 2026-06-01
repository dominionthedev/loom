// Package storage manages all runtime operational data.
// Everything persists under .loom/ in the workspace root.
//
//	.loom/
//	  artifacts/   — named outputs + sidecar .meta.json
//	  logs/        — execution run records
//	  state/       — runtime state
//	  sessions/    — session data (v0.4)
//	  worktrees/   — worktree copies (v0.3)
package storage

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Store is the root storage system.
type Store struct {
	root string // absolute path to .loom/
}

// New opens or creates a store at workspaceDir/.loom/.
func New(workspaceDir string) (*Store, error) {
	root := filepath.Join(workspaceDir, ".loom")
	for _, sub := range []string{"artifacts", "logs", "state", "sessions", "worktrees"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
			return nil, fmt.Errorf("storage: init %s: %w", sub, err)
		}
	}
	return &Store{root: root}, nil
}

// Root returns the .loom/ directory path.
func (s *Store) Root() string { return s.root }

// ArtifactsDir returns the artifacts directory path.
func (s *Store) ArtifactsDir() string { return filepath.Join(s.root, "artifacts") }

// ── Artifacts ─────────────────────────────────────────────────────────────

// ArtifactMeta is the sidecar metadata for an artifact.
type ArtifactMeta struct {
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	Task      string `json:"task"`
	Step      string `json:"step"`
	RunID     string `json:"run_id"`
	CreatedAt string `json:"created_at"`
}

// SaveArtifact writes an artifact file + sidecar .meta.json to .loom/artifacts/.
func (s *Store) SaveArtifact(runID, task, step, name, kind, content string) error {
	dir := s.ArtifactsDir()
	contentPath := filepath.Join(dir, name)

	if err := os.WriteFile(contentPath, []byte(content), 0o644); err != nil {
		return fmt.Errorf("storage: artifact write: %w", err)
	}

	meta := ArtifactMeta{
		Name:      name,
		Kind:      kind,
		Task:      task,
		Step:      step,
		RunID:     runID,
		CreatedAt: time.Now().Format(time.RFC3339),
	}
	metaData, _ := json.MarshalIndent(meta, "", "  ")
	if err := os.WriteFile(contentPath+".meta.json", metaData, 0o644); err != nil {
		return fmt.Errorf("storage: artifact meta write: %w", err)
	}
	return nil
}

// ReadArtifact reads an artifact's content by name.
func (s *Store) ReadArtifact(name string) (string, error) {
	path := filepath.Join(s.ArtifactsDir(), name)
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("storage: read artifact %q: %w", name, err)
	}
	return string(data), nil
}

// ListArtifacts returns all artifact names (excluding sidecar files).
func (s *Store) ListArtifacts() ([]string, error) {
	entries, err := os.ReadDir(s.ArtifactsDir())
	if err != nil {
		return nil, fmt.Errorf("storage: list artifacts: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && !strings.HasSuffix(e.Name(), ".meta.json") {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// ── Run history ───────────────────────────────────────────────────────────

// RunRecord is a summary of one file execution.
type RunRecord struct {
	RunID      string    `json:"run_id"`
	File       string    `json:"file"`
	Success    bool      `json:"success"`
	Tasks      int       `json:"tasks"`
	Error      string    `json:"error,omitempty"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
}

// RecordRun persists a run record to .loom/logs/.
func (s *Store) RecordRun(r RunRecord) {
	data, _ := json.MarshalIndent(r, "", "  ")
	path := filepath.Join(s.root, "logs", r.RunID+".json")
	_ = os.WriteFile(path, data, 0o644)
}

// ── State ─────────────────────────────────────────────────────────────────

// SaveState persists arbitrary state JSON under .loom/state/<key>.json.
func (s *Store) SaveState(key string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("storage: state marshal: %w", err)
	}
	return os.WriteFile(filepath.Join(s.root, "state", key+".json"), data, 0o644)
}

// LoadState reads state JSON from .loom/state/<key>.json into v.
func (s *Store) LoadState(key string, v any) error {
	data, err := os.ReadFile(filepath.Join(s.root, "state", key+".json"))
	if err != nil {
		return fmt.Errorf("storage: state read %q: %w", key, err)
	}
	return json.Unmarshal(data, v)
}
