package training

import (
	"context"
	"fmt"
	"time"

	"github.com/example/ai-assistants-platform/internal/ai/providers"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

type Memory struct {
	ID        uuid.UUID
	Content   string
	Score     float64
	CreatedAt time.Time
}

type Service struct {
	db *pgxpool.Pool
	p  providers.Provider
}

func NewService(db *pgxpool.Pool, p providers.Provider) *Service {
	return &Service{db: db, p: p}
}

func (s *Service) Learn(ctx context.Context, org, assistant, conversation, message, user uuid.UUID, content string) (uuid.UUID, error) {
	embeddings, err := s.p.Embed(ctx, []string{content})
	if err != nil {
		return uuid.Nil, fmt.Errorf("embed training message: %w", err)
	}
	if len(embeddings) != 1 || len(embeddings[0]) == 0 {
		return uuid.Nil, fmt.Errorf("embed training message: provider returned no embedding")
	}
	var id uuid.UUID
	err = s.db.QueryRow(ctx, `INSERT INTO assistant_training_entries(organization_id,assistant_id,conversation_id,source_message_id,created_by,content,embedding) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(source_message_id) DO UPDATE SET content=EXCLUDED.content,embedding=EXCLUDED.embedding RETURNING id`, org, assistant, conversation, message, user, content, pgvector.NewVector(embeddings[0])).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("save training message: %w", err)
	}
	return id, nil
}

func (s *Service) Retrieve(ctx context.Context, org, assistant uuid.UUID, query string, topK int, minScore float64) ([]Memory, error) {
	if topK <= 0 {
		return nil, nil
	}
	var exists bool
	if err := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM assistant_training_entries ate JOIN conversations c ON c.organization_id=ate.organization_id AND c.id=ate.conversation_id WHERE ate.organization_id=$1 AND ate.assistant_id=$2 AND c.status='active')`, org, assistant).Scan(&exists); err != nil {
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
	rows, err := s.db.Query(ctx, `SELECT ate.id,ate.content,1-(ate.embedding <=> $3) score,ate.created_at FROM assistant_training_entries ate JOIN conversations c ON c.organization_id=ate.organization_id AND c.id=ate.conversation_id WHERE ate.organization_id=$1 AND ate.assistant_id=$2 AND c.status='active' AND 1-(ate.embedding <=> $3) >= $4 ORDER BY ate.embedding <=> $3,ate.created_at DESC LIMIT $5`, org, assistant, vector, minScore, topK)
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
