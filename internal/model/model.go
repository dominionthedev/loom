// Package model provides the LLM abstraction.
// The rest of Loom only depends on the Model interface.
package model

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
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

// Config holds model routing configuration.
// Loaded from loom.lua config — maps think levels to model names.
type Config struct {
	Default string // model for think("low") or no think()
	Medium  string // model for think("medium")
	High    string // model for think("high")
}

// DefaultConfig returns a config pointing everything at the same model.
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
	clients map[string]Model // model name → client
	baseURL string
}

// NewRouter returns a Router. All models talk to the same Ollama server.
func NewRouter(cfg Config) *Router {
	base := firstEnv("OLLAMA_HOST", "OLLACLOUD_HOST")
	if base == "" {
		base = "http://localhost:11434"
	}
	r := &Router{
		cfg:     cfg,
		clients: make(map[string]Model),
		baseURL: base,
	}
	return r
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

// Default returns the default model (no think() specified).
func (r *Router) Default() Model {
	return r.For("")
}

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
		// Explicit model name passed directly.
		return level
	}
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
		client:  &http.Client{Timeout: 120 * time.Second},
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
		return "", fmt.Errorf("model: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("model: request: %w", err)
	}
	defer resp.Body.Close()

	var out ollamaResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("model: decode: %w", err)
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
