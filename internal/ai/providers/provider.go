package providers

import (
	"context"
	"fmt"
	"sync"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type ChatRequest struct {
	Model       string
	Messages    []Message
	Temperature float32
	MaxTokens   int
}
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}
type ChatChunk struct {
	Delta string
	Usage Usage
	Done  bool
	Err   error
}
type Provider interface {
	StreamChat(context.Context, ChatRequest) (<-chan ChatChunk, error)
	Embed(context.Context, []string) ([][]float32, error)
}
type Registry struct {
	mu sync.RWMutex
	p  map[string]Provider
}

func NewRegistry() *Registry                         { return &Registry{p: map[string]Provider{}} }
func (r *Registry) Register(name string, p Provider) { r.mu.Lock(); defer r.mu.Unlock(); r.p[name] = p }
func (r *Registry) Get(name string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.p[name]
	if !ok {
		return nil, fmt.Errorf("provider %q is not configured", name)
	}
	return p, nil
}
