package training

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/example/ai-assistants-platform/internal/ai/providers"
	"github.com/example/ai-assistants-platform/internal/auth"
	"github.com/example/ai-assistants-platform/internal/platform/response"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

type Memory struct {
	ID        uuid.UUID
	Content   string
	Score     float64
	CreatedAt time.Time
}

type Entry struct {
	ID                uuid.UUID  `json:"id"`
	AssistantID       uuid.UUID  `json:"assistant_id"`
	AssistantName     string     `json:"assistant_name"`
	ConversationID    uuid.UUID  `json:"conversation_id"`
	ConversationTitle string     `json:"conversation_title"`
	SourceMessageID   uuid.UUID  `json:"source_message_id"`
	CreatedBy         uuid.UUID  `json:"created_by"`
	CreatedByName     string     `json:"created_by_name"`
	Content           string     `json:"content"`
	Status            string     `json:"status"`
	Scope             string     `json:"scope"`
	Version           int        `json:"version"`
	ReviewedBy        *uuid.UUID `json:"reviewed_by"`
	ReviewedAt        *time.Time `json:"reviewed_at"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type Service struct {
	db *pgxpool.Pool
	p  providers.Provider
}

func NewService(db *pgxpool.Pool, p providers.Provider) *Service {
	return &Service{db: db, p: p}
}

func (s *Service) Learn(ctx context.Context, actor auth.Actor, assistant, conversation, message uuid.UUID, content string) (Entry, error) {
	if !actor.CanTrainAssistant() {
		return Entry{}, response.E(403, "TRAINING_FORBIDDEN", "You do not have permission to train assistants")
	}
	embeddings, err := s.p.Embed(ctx, []string{content})
	if err != nil {
		return Entry{}, fmt.Errorf("embed training message: %w", err)
	}
	if len(embeddings) != 1 || len(embeddings[0]) == 0 {
		return Entry{}, fmt.Errorf("embed training message: provider returned no embedding")
	}
	status := "pending"
	var reviewedBy any
	var reviewedAt any
	if actor.IsAdmin() {
		status = "approved"
		reviewedBy = actor.UserID
		reviewedAt = time.Now()
	}
	var id uuid.UUID
	err = s.db.QueryRow(ctx, `INSERT INTO assistant_training_entries(organization_id,assistant_id,conversation_id,source_message_id,created_by,content,embedding,status,scope,reviewed_by,reviewed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'organization',$9,$10) ON CONFLICT(source_message_id) DO UPDATE SET content=EXCLUDED.content,embedding=EXCLUDED.embedding,status=EXCLUDED.status,reviewed_by=EXCLUDED.reviewed_by,reviewed_at=EXCLUDED.reviewed_at,version=assistant_training_entries.version+1,updated_at=now() RETURNING id`, actor.OrganizationID, assistant, conversation, message, actor.UserID, content, pgvector.NewVector(embeddings[0]), status, reviewedBy, reviewedAt).Scan(&id)
	if err != nil {
		return Entry{}, fmt.Errorf("save training message: %w", err)
	}
	return s.Get(ctx, actor, id)
}

func (s *Service) Retrieve(ctx context.Context, org, assistant, user uuid.UUID, query string, topK int, minScore float64) ([]Memory, error) {
	if topK <= 0 {
		return nil, nil
	}
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assistant_training_entries WHERE organization_id=$1 AND assistant_id=$2 AND status='approved' AND (scope='organization' OR (scope='private' AND created_by=$3)))`, org, assistant, user).Scan(&exists); err != nil {
		return nil, err
	}
	if !exists {
		return nil, nil
	}
	embeddings, err := s.p.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("embed training query: %w", err)
	}
	if len(embeddings) != 1 || len(embeddings[0]) == 0 {
		return nil, fmt.Errorf("embed training query: provider returned no embedding")
	}
	vector := pgvector.NewVector(embeddings[0])
	rows, err := s.db.Query(ctx, `SELECT id,content,1-(embedding <=> $4) score,created_at FROM assistant_training_entries WHERE organization_id=$1 AND assistant_id=$2 AND status='approved' AND (scope='organization' OR (scope='private' AND created_by=$3)) AND 1-(embedding <=> $4) >= $5 ORDER BY embedding <=> $4,created_at DESC LIMIT $6`, org, assistant, user, vector, minScore, topK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Memory
	for rows.Next() {
		var memory Memory
		if err = rows.Scan(&memory.ID, &memory.Content, &memory.Score, &memory.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, memory)
	}
	return out, rows.Err()
}

func (s *Service) List(ctx context.Context, actor auth.Actor, page, limit int, assistantID *uuid.UUID, status, scope string) ([]Entry, int, error) {
	if !actor.IsAdmin() {
		return nil, 0, response.ErrForbidden
	}
	if status != "" && !validStatus(status) {
		return nil, 0, response.E(422, "VALIDATION_ERROR", "Invalid training status")
	}
	if scope != "" && !validScope(scope) {
		return nil, 0, response.E(422, "VALIDATION_ERROR", "Invalid training scope")
	}
	rows, err := s.db.Query(ctx, `SELECT ate.id,ate.assistant_id,a.name,ate.conversation_id,c.title,ate.source_message_id,ate.created_by,u.name,ate.content,ate.status,ate.scope,ate.version,ate.reviewed_by,ate.reviewed_at,ate.created_at,ate.updated_at,count(*) OVER() FROM assistant_training_entries ate JOIN assistants a ON a.organization_id=ate.organization_id AND a.id=ate.assistant_id JOIN conversations c ON c.organization_id=ate.organization_id AND c.id=ate.conversation_id JOIN users u ON u.id=ate.created_by WHERE ate.organization_id=$1 AND ($2::uuid IS NULL OR ate.assistant_id=$2) AND ($3='' OR ate.status::text=$3) AND ($4='' OR ate.scope=$4) ORDER BY ate.created_at DESC LIMIT $5 OFFSET $6`, actor.OrganizationID, assistantID, status, scope, limit, (page-1)*limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Entry
	total := 0
	for rows.Next() {
		var entry Entry
		if err = scanEntry(rows, &entry, &total); err != nil {
			return nil, 0, err
		}
		out = append(out, entry)
	}
	return out, total, rows.Err()
}

func (s *Service) Get(ctx context.Context, actor auth.Actor, id uuid.UUID) (Entry, error) {
	var entry Entry
	err := s.db.QueryRow(ctx, `SELECT ate.id,ate.assistant_id,a.name,ate.conversation_id,c.title,ate.source_message_id,ate.created_by,u.name,ate.content,ate.status,ate.scope,ate.version,ate.reviewed_by,ate.reviewed_at,ate.created_at,ate.updated_at FROM assistant_training_entries ate JOIN assistants a ON a.organization_id=ate.organization_id AND a.id=ate.assistant_id JOIN conversations c ON c.organization_id=ate.organization_id AND c.id=ate.conversation_id JOIN users u ON u.id=ate.created_by WHERE ate.organization_id=$1 AND ate.id=$2 AND ($3 OR ate.created_by=$4)`, actor.OrganizationID, id, actor.IsAdmin(), actor.UserID).Scan(&entry.ID, &entry.AssistantID, &entry.AssistantName, &entry.ConversationID, &entry.ConversationTitle, &entry.SourceMessageID, &entry.CreatedBy, &entry.CreatedByName, &entry.Content, &entry.Status, &entry.Scope, &entry.Version, &entry.ReviewedBy, &entry.ReviewedAt, &entry.CreatedAt, &entry.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, response.ErrNotFound
	}
	return entry, err
}

func (s *Service) Review(ctx context.Context, actor auth.Actor, id uuid.UUID, status, scope string, content *string) (Entry, error) {
	if !actor.IsAdmin() {
		return Entry{}, response.ErrForbidden
	}
	if !validStatus(status) || !validScope(scope) {
		return Entry{}, response.E(422, "VALIDATION_ERROR", "status and scope are invalid")
	}
	var embedding any
	if content != nil {
		trimmed := strings.TrimSpace(*content)
		if trimmed == "" || len(trimmed) > 100000 {
			return Entry{}, response.E(422, "VALIDATION_ERROR", "Training content is invalid")
		}
		content = &trimmed
		embeddings, err := s.p.Embed(ctx, []string{trimmed})
		if err != nil {
			return Entry{}, fmt.Errorf("embed reviewed training message: %w", err)
		}
		if len(embeddings) != 1 || len(embeddings[0]) == 0 {
			return Entry{}, fmt.Errorf("embed reviewed training message: provider returned no embedding")
		}
		embedding = pgvector.NewVector(embeddings[0])
	}
	tag, err := s.db.Exec(ctx, `UPDATE assistant_training_entries SET content=COALESCE($5,content),embedding=COALESCE($6,embedding),status=$3,scope=$4,reviewed_by=$7,reviewed_at=now(),version=version+1,updated_at=now() WHERE organization_id=$1 AND id=$2`, actor.OrganizationID, id, status, scope, content, embedding, actor.UserID)
	if err != nil {
		return Entry{}, err
	}
	if tag.RowsAffected() == 0 {
		return Entry{}, response.ErrNotFound
	}
	return s.Get(ctx, actor, id)
}

func (s *Service) Delete(ctx context.Context, actor auth.Actor, id uuid.UUID) error {
	if !actor.IsAdmin() {
		return response.ErrForbidden
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM assistant_training_entries WHERE organization_id=$1 AND id=$2`, actor.OrganizationID, id)
	if err == nil && tag.RowsAffected() == 0 {
		return response.ErrNotFound
	}
	return err
}

func validStatus(status string) bool {
	return status == "pending" || status == "approved" || status == "rejected"
}

func validScope(scope string) bool { return scope == "private" || scope == "organization" }

type scanner interface{ Scan(...any) error }

func scanEntry(row scanner, entry *Entry, total *int) error {
	return row.Scan(&entry.ID, &entry.AssistantID, &entry.AssistantName, &entry.ConversationID, &entry.ConversationTitle, &entry.SourceMessageID, &entry.CreatedBy, &entry.CreatedByName, &entry.Content, &entry.Status, &entry.Scope, &entry.Version, &entry.ReviewedBy, &entry.ReviewedAt, &entry.CreatedAt, &entry.UpdatedAt, total)
}
