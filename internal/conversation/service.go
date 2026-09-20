package conversation

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/example/ai-assistants-platform/internal/ai"
	"github.com/example/ai-assistants-platform/internal/assistant"
	"github.com/example/ai-assistants-platform/internal/auth"
	"github.com/example/ai-assistants-platform/internal/platform/response"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Conversation struct {
	ID, AssistantID, UserID uuid.UUID
	Title, Status, Mode     string
	CreatedAt, UpdatedAt    time.Time
}

func (c Conversation) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"id": c.ID, "assistant_id": c.AssistantID, "user_id": c.UserID, "title": c.Title, "status": c.Status, "mode": c.Mode, "created_at": c.CreatedAt, "updated_at": c.UpdatedAt})
}

type Message struct {
	ID                                     uuid.UUID
	Role, Content                          string
	Provider, Model                        *string
	InputTokens, OutputTokens, TotalTokens int
	LatencyMS                              int64
	Metadata                               json.RawMessage
	CreatedAt                              time.Time
	Feedback                               *MessageFeedback
}

type MessageFeedback struct {
	ID     uuid.UUID `json:"id"`
	Rating int16     `json:"rating"`
	Status string    `json:"status"`
}

func (m Message) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"id": m.ID, "role": m.Role, "content": m.Content, "provider": m.Provider, "model": m.Model, "input_tokens": m.InputTokens, "output_tokens": m.OutputTokens, "total_tokens": m.TotalTokens, "latency_ms": m.LatencyMS, "metadata": json.RawMessage(m.Metadata), "feedback": m.Feedback, "created_at": m.CreatedAt})
}

type Detail struct {
	Conversation Conversation `json:"conversation"`
	Messages     []Message    `json:"messages"`
}
type Service struct {
	db         *pgxpool.Pool
	assistants *assistant.Service
	ai         ai.Orchestrator
}

func NewService(db *pgxpool.Pool, a *assistant.Service, o ai.Orchestrator) *Service {
	return &Service{db: db, assistants: a, ai: o}
}
func (s *Service) Create(ctx context.Context, a auth.Actor, assistantID uuid.UUID, title, mode string) (Conversation, error) {
	if _, err := s.assistants.Get(ctx, a, assistantID); err != nil {
		return Conversation{}, err
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = "New conversation"
	}
	if len(title) > 300 {
		return Conversation{}, response.E(422, "VALIDATION_ERROR", "Title is too long")
	}
	if mode == "" {
		mode = "work"
	}
	if mode != "work" && mode != "train" {
		return Conversation{}, response.E(422, "VALIDATION_ERROR", "mode must be work or train")
	}
	if mode == "train" && !a.CanTrainAssistant() {
		return Conversation{}, response.E(403, "TRAINING_FORBIDDEN", "You do not have permission to train assistants")
	}
	var c Conversation
	err := s.db.QueryRow(ctx, `INSERT INTO conversations(organization_id,assistant_id,user_id,title,mode) VALUES($1,$2,$3,$4,$5) RETURNING id,assistant_id,user_id,title,status,mode,created_at,updated_at`, a.OrganizationID, assistantID, a.UserID, title, mode).Scan(&c.ID, &c.AssistantID, &c.UserID, &c.Title, &c.Status, &c.Mode, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}
func (s *Service) List(ctx context.Context, a auth.Actor, page, limit int, assistantID *uuid.UUID, search, sort string) ([]Conversation, int, error) {
	order := "updated_at DESC"
	if sort == "created_at" {
		order = "created_at DESC"
	} else if sort == "title" {
		order = "title ASC"
	}
	q := `SELECT id,assistant_id,user_id,title,status,mode,created_at,updated_at,count(*) OVER() FROM conversations WHERE organization_id=$1 AND status='active' AND ($2 OR user_id=$3) AND ($4::uuid IS NULL OR assistant_id=$4) AND ($5='' OR title ILIKE '%'||$5||'%') ORDER BY ` + order + ` LIMIT $6 OFFSET $7`
	rows, err := s.db.Query(ctx, q, a.OrganizationID, a.IsAdmin(), a.UserID, assistantID, search, limit, (page-1)*limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Conversation
	total := 0
	for rows.Next() {
		var c Conversation
		if err = rows.Scan(&c.ID, &c.AssistantID, &c.UserID, &c.Title, &c.Status, &c.Mode, &c.CreatedAt, &c.UpdatedAt, &total); err != nil {
			return nil, 0, err
		}
		out = append(out, c)
	}
	return out, total, rows.Err()
}
func (s *Service) Get(ctx context.Context, a auth.Actor, id uuid.UUID) (Detail, error) {
	var d Detail
	err := s.db.QueryRow(ctx, `SELECT id,assistant_id,user_id,title,status,mode,created_at,updated_at FROM conversations WHERE organization_id=$1 AND id=$2 AND status='active' AND ($3 OR user_id=$4)`, a.OrganizationID, id, a.IsAdmin(), a.UserID).Scan(&d.Conversation.ID, &d.Conversation.AssistantID, &d.Conversation.UserID, &d.Conversation.Title, &d.Conversation.Status, &d.Conversation.Mode, &d.Conversation.CreatedAt, &d.Conversation.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return d, response.ErrNotFound
	}
	if err != nil {
		return d, err
	}
	rows, err := s.db.Query(ctx, `SELECT m.id,m.role,m.content,m.provider,m.model,m.input_tokens,m.output_tokens,m.total_tokens,m.latency_ms,m.metadata,m.created_at,mf.id,mf.rating,mf.status FROM messages m LEFT JOIN message_feedback mf ON mf.organization_id=m.organization_id AND mf.message_id=m.id AND mf.user_id=$3 WHERE m.organization_id=$1 AND m.conversation_id=$2 ORDER BY m.created_at,m.id`, a.OrganizationID, id, a.UserID)
	if err != nil {
		return d, err
	}
	defer rows.Close()
	for rows.Next() {
		var m Message
		var feedbackID *uuid.UUID
		var feedbackRating *int16
		var feedbackStatus *string
		if err = rows.Scan(&m.ID, &m.Role, &m.Content, &m.Provider, &m.Model, &m.InputTokens, &m.OutputTokens, &m.TotalTokens, &m.LatencyMS, &m.Metadata, &m.CreatedAt, &feedbackID, &feedbackRating, &feedbackStatus); err != nil {
			return d, err
		}
		if feedbackID != nil && feedbackRating != nil && feedbackStatus != nil {
			m.Feedback = &MessageFeedback{ID: *feedbackID, Rating: *feedbackRating, Status: *feedbackStatus}
		}
		d.Messages = append(d.Messages, m)
	}
	return d, rows.Err()
}
func (s *Service) Update(ctx context.Context, a auth.Actor, id uuid.UUID, title, mode *string) (Conversation, error) {
	if title == nil && mode == nil {
		return Conversation{}, response.E(422, "VALIDATION_ERROR", "title or mode is required")
	}
	if title != nil {
		trimmed := strings.TrimSpace(*title)
		if trimmed == "" || len(trimmed) > 300 {
			return Conversation{}, response.E(422, "VALIDATION_ERROR", "Invalid title")
		}
		title = &trimmed
	}
	if mode != nil && *mode != "work" && *mode != "train" {
		return Conversation{}, response.E(422, "VALIDATION_ERROR", "mode must be work or train")
	}
	if mode != nil && *mode == "train" && !a.CanTrainAssistant() {
		return Conversation{}, response.E(403, "TRAINING_FORBIDDEN", "You do not have permission to train assistants")
	}
	var c Conversation
	err := s.db.QueryRow(ctx, `UPDATE conversations SET title=COALESCE($5,title),mode=COALESCE($6,mode),updated_at=now() WHERE organization_id=$1 AND id=$2 AND status='active' AND ($3 OR user_id=$4) RETURNING id,assistant_id,user_id,title,status,mode,created_at,updated_at`, a.OrganizationID, id, a.IsAdmin(), a.UserID, title, mode).Scan(&c.ID, &c.AssistantID, &c.UserID, &c.Title, &c.Status, &c.Mode, &c.CreatedAt, &c.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = response.ErrNotFound
	}
	return c, err
}
func (s *Service) Delete(ctx context.Context, a auth.Actor, id uuid.UUID) error {
	tag, err := s.db.Exec(ctx, `UPDATE conversations SET status='deleted',updated_at=now() WHERE organization_id=$1 AND id=$2 AND status='active' AND ($3 OR user_id=$4)`, a.OrganizationID, id, a.IsAdmin(), a.UserID)
	if err == nil && tag.RowsAffected() == 0 {
		return response.ErrNotFound
	}
	return err
}
func (s *Service) Send(ctx context.Context, a auth.Actor, id uuid.UUID, content string) (<-chan ai.StreamEvent, error) {
	content = strings.TrimSpace(content)
	if content == "" || len(content) > 100000 {
		return nil, response.E(422, "VALIDATION_ERROR", "Message content is required and must be at most 100000 characters")
	}
	var mode string
	err := s.db.QueryRow(ctx, `SELECT mode FROM conversations WHERE organization_id=$1 AND id=$2 AND status='active' AND ($3 OR user_id=$4)`, a.OrganizationID, id, a.IsAdmin(), a.UserID).Scan(&mode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, response.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	if mode == "train" && !a.CanTrainAssistant() {
		return nil, response.E(403, "TRAINING_FORBIDDEN", "You do not have permission to train assistants")
	}
	mid := uuid.New()
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `INSERT INTO messages(id,organization_id,conversation_id,role,content,metadata) SELECT $1,$2,$3,'user',$4,jsonb_build_object('conversation_mode',mode) FROM conversations WHERE organization_id=$2 AND id=$3`, mid, a.OrganizationID, id, content)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(ctx, `UPDATE conversations SET title=CASE WHEN title='New conversation' THEN left($3,80) ELSE title END,updated_at=now() WHERE organization_id=$1 AND id=$2`, a.OrganizationID, id, content)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return nil, err
	}
	return s.ai.Generate(ctx, ai.GenerateRequest{Actor: a, ConversationID: id, UserMessageID: mid, Content: content})
}
