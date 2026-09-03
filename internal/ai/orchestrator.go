package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/example/ai-assistants-platform/internal/ai/providers"
	"github.com/example/ai-assistants-platform/internal/assistant"
	"github.com/example/ai-assistants-platform/internal/auth"
	"github.com/example/ai-assistants-platform/internal/feedback"
	"github.com/example/ai-assistants-platform/internal/knowledge"
	"github.com/example/ai-assistants-platform/internal/platform/response"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/sync/errgroup"
)

type GenerateRequest struct {
	Actor                         auth.Actor
	ConversationID, UserMessageID uuid.UUID
	Content                       string
}
type StreamEvent struct {
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
	Err  error  `json:"-"`
}
type Orchestrator interface {
	Generate(context.Context, GenerateRequest) (<-chan StreamEvent, error)
}
type Engine struct {
	db         *pgxpool.Pool
	assistants *assistant.Service
	knowledge  knowledge.KnowledgeRetriever
	feedback   *feedback.Service
	providers  *providers.Registry
	builder    ContextBuilder
	log        *slog.Logger
}

func NewEngine(db *pgxpool.Pool, a *assistant.Service, k knowledge.KnowledgeRetriever, f *feedback.Service, p *providers.Registry, log *slog.Logger) *Engine {
	return &Engine{db: db, assistants: a, knowledge: k, feedback: f, providers: p, builder: ContextBuilder{}, log: log}
}
func (e *Engine) Generate(ctx context.Context, req GenerateRequest) (<-chan StreamEvent, error) {
	var assistantID uuid.UUID
	err := e.db.QueryRow(ctx, `SELECT assistant_id FROM conversations WHERE organization_id=$1 AND id=$2 AND status='active' AND ($3 OR user_id=$4)`, req.Actor.OrganizationID, req.ConversationID, req.Actor.IsAdmin(), req.Actor.UserID).Scan(&assistantID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, response.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	asst, err := e.assistants.Get(ctx, req.Actor, assistantID)
	if err != nil {
		return nil, err
	}
	settings := asst.ParsedSettings()
	history, err := e.history(ctx, req, settings.History.MessageLimit)
	if err != nil {
		return nil, err
	}
	var chunks []knowledge.KnowledgeChunk
	var examples []feedback.Example
	g, gctx := errgroup.WithContext(ctx)
	if settings.RAG.Enabled {
		g.Go(func() error {
			var x error
			chunks, x = e.knowledge.Retrieve(gctx, req.Actor.OrganizationID, assistantID, req.Content, knowledge.RetrieveOptions{TopK: settings.RAG.TopK, MinScore: settings.RAG.MinScore})
			return x
		})
	}
	if settings.Feedback.Enabled {
		g.Go(func() error {
			var x error
			examples, x = e.feedback.Retrieve(gctx, req.Actor.OrganizationID, assistantID, req.Content, settings.Feedback.TopK)
			return x
		})
	}
	if err = g.Wait(); err != nil {
		return nil, fmt.Errorf("context retrieval: %w", err)
	}
	provider, err := e.providers.Get(asst.Provider)
	if err != nil {
		return nil, err
	}
	messages := e.builder.Build(asst, history, chunks, examples, req.Content)
	stream, err := provider.StreamChat(ctx, providers.ChatRequest{Model: asst.Model, Messages: messages, Temperature: asst.Temperature, MaxTokens: asst.MaxOutputTokens})
	if err != nil {
		return nil, err
	}
	out := make(chan StreamEvent)
	go e.relay(ctx, out, stream, req, asst, chunks)
	return out, nil
}
func (e *Engine) history(ctx context.Context, req GenerateRequest, limit int) ([]HistoryMessage, error) {
	rows, err := e.db.Query(ctx, `SELECT role,content FROM (SELECT role,content,created_at,id FROM messages WHERE organization_id=$1 AND conversation_id=$2 AND id<>$3 ORDER BY created_at DESC,id DESC LIMIT $4) x ORDER BY created_at,id`, req.Actor.OrganizationID, req.ConversationID, req.UserMessageID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HistoryMessage
	for rows.Next() {
		var x HistoryMessage
		if err = rows.Scan(&x.Role, &x.Content); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (e *Engine) relay(ctx context.Context, out chan StreamEvent, in <-chan providers.ChatChunk, req GenerateRequest, a assistant.Assistant, chunks []knowledge.KnowledgeChunk) {
	defer close(out)
	start := time.Now()
	id := uuid.New()
	send := func(x StreamEvent) bool {
		select {
		case out <- x:
			return true
		case <-ctx.Done():
			return false
		}
	}
	if !send(StreamEvent{Type: "message_start", Data: map[string]any{"message_id": id}}) {
		return
	}
	if a.ParsedSettings().Citations && len(chunks) > 0 {
		src := make([]map[string]any, len(chunks))
		for i, c := range chunks {
			src[i] = c.Source()
		}
		if !send(StreamEvent{Type: "sources", Data: src}) {
			return
		}
	}
	var text strings.Builder
	var usage providers.Usage
	for c := range in {
		if c.Err != nil {
			send(StreamEvent{Type: "error", Data: map[string]string{"code": "PROVIDER_STREAM_ERROR", "message": "AI provider stream failed"}, Err: c.Err})
			return
		}
		if c.Delta != "" {
			text.WriteString(c.Delta)
			if !send(StreamEvent{Type: "content_delta", Data: map[string]string{"delta": c.Delta}}) {
				return
			}
		}
		if c.Usage.TotalTokens > 0 {
			usage = c.Usage
		}
	}
	if ctx.Err() != nil {
		return
	}
	src := make([]map[string]any, len(chunks))
	for i, c := range chunks {
		src[i] = c.Source()
	}
	meta, _ := json.Marshal(map[string]any{"sources": src})
	tx, err := e.db.Begin(ctx)
	if err == nil {
		_, err = tx.Exec(ctx, `INSERT INTO messages(id,organization_id,conversation_id,role,content,provider,model,input_tokens,output_tokens,total_tokens,latency_ms,metadata) VALUES($1,$2,$3,'assistant',$4,$5,$6,$7,$8,$9,$10,$11)`, id, req.Actor.OrganizationID, req.ConversationID, text.String(), a.Provider, a.Model, usage.InputTokens, usage.OutputTokens, usage.TotalTokens, time.Since(start).Milliseconds(), meta)
	}
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE conversations SET updated_at=now() WHERE organization_id=$1 AND id=$2`, req.Actor.OrganizationID, req.ConversationID)
	}
	if err == nil {
		err = tx.Commit(ctx)
	} else if tx != nil {
		_ = tx.Rollback(ctx)
	}
	if err != nil {
		e.log.Error("persist assistant message", "error", err, "conversation_id", req.ConversationID)
		send(StreamEvent{Type: "error", Data: map[string]string{"code": "PERSISTENCE_ERROR", "message": "Response could not be saved"}, Err: err})
		return
	}
	e.log.Info("llm response", "provider", a.Provider, "model", a.Model, "latency_ms", time.Since(start).Milliseconds(), "input_tokens", usage.InputTokens, "output_tokens", usage.OutputTokens)
	send(StreamEvent{Type: "message_complete", Data: map[string]any{"message_id": id, "usage": usage}})
}
