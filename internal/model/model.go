// Package model provides the LLM abstraction.
// The rest of Loom only depends on the Model interface.
package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// Turn is one message in a conversation.
type Turn struct {
	Role    string // "user" | "assistant"
	Content string
}

// Model is the only LLM interface Loom uses.
type Model interface {
	Chat(ctx context.Context, system string, history []Turn, input string) (string, error)
}

// ModelInfo is returned by ListModels.
type ModelInfo struct {
	Name string `json:"name"`
	Size int64  `json:"size"`
}

// Config holds model routing configuration.
// Maps think levels to model names.
type Config struct {
	Default string // think("low") or no think()
	Medium  string // think("medium")
	High    string // think("high") — used by plan() always
}

// DefaultConfig points all levels at the same model.
func DefaultConfig(modelName string) Config {
	return Config{
		Default: modelName,
		Medium:  modelName,
		High:    modelName,
	}
}

// Router selects the right model based on think level.
type Router struct {
	cfg     Config
	clients map[string]Model
	baseURL string
}

// NewRouter returns a Router. All models talk to the same Ollama server.
func NewRouter(cfg Config) *Router {
	base := firstEnv("OLLAMA_HOST", "OLLACLOUD_HOST")
	if base == "" {
		base = "http://localhost:11434"
	}
	return &Router{
		cfg:     cfg,
		clients: make(map[string]Model),
		baseURL: base,
	}
}

// For returns the model for a given think level or explicit model name.
func (r *Router) For(thinkLevel string) Model {
	name := r.resolve(thinkLevel)
	if m, ok := r.clients[name]; ok {
		return m
	}
	m := newOllama(r.baseURL, name)
	r.clients[name] = m
	return m
}

// Default returns the default model.
func (r *Router) Default() Model { return r.For("") }

// BaseURL returns the server base URL.
func (r *Router) BaseURL() string { return r.baseURL }

func (r *Router) resolve(level string) string {
	switch level {
	case "", "low":
		return r.cfg.Default
	case "medium":
		if r.cfg.Medium != "" {
			return r.cfg.Medium
		}
		return r.cfg.Default
	case "high":
		if r.cfg.High != "" {
			return r.cfg.High
		}
		return r.cfg.Default
	default:
		return level // explicit model name
	}
}

// ListModels queries /api/tags and returns available models.
func ListModels(baseURL string) ([]ModelInfo, error) {
	if baseURL == "" {
		baseURL = firstEnv("OLLAMA_HOST", "OLLACLOUD_HOST")
	}
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(baseURL + "/api/tags")
	if err != nil {
		return nil, fmt.Errorf("model: list: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("model: list: server returned %d", resp.StatusCode)
	}

	var result struct {
		Models []ModelInfo `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("model: list: decode: %w", err)
	}
	return result.Models, nil
}

// ── Ollama ────────────────────────────────────────────────────────────────

type ollamaModel struct {
	baseURL string
	name    string
	client  *http.Client
}

func newOllama(baseURL, name string) *ollamaModel {
	return &ollamaModel{
		baseURL: baseURL,
		name:    name,
		client:  &http.Client{Timeout: 180 * time.Second},
	}
}

type ollamaReq struct {
	Model    string      `json:"model"`
	Messages []ollamaMsg `json:"messages"`
	Stream   bool        `json:"stream"`
}

type ollamaMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type ollamaResp struct {
	Message ollamaMsg `json:"message"`
}

func (m *ollamaModel) Chat(ctx context.Context, system string, history []Turn, input string) (string, error) {
	msgs := []ollamaMsg{{Role: "system", Content: system}}
	for _, t := range history {
		msgs = append(msgs, ollamaMsg{Role: t.Role, Content: t.Content})
	}
	msgs = append(msgs, ollamaMsg{Role: "user", Content: input})

	body, _ := json.Marshal(ollamaReq{
		Model:    m.name,
		Messages: msgs,
		Stream:   false,
	})

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		m.baseURL+"/api/chat", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("model: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("model: request failed: %w", err)
	}
	defer resp.Body.Close()

	// ── Non-2xx is a real error — don't silently decode garbage ──────
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("model: server returned %d for model %q: %s",
			resp.StatusCode, m.name, strings.TrimSpace(string(errBody)))
	}

	var out ollamaResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("model: decode response: %w", err)
	}
	return strings.TrimSpace(out.Message.Content), nil
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}
