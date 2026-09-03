package knowledge

import (
	"context"
	"encoding/json"

	"github.com/example/ai-assistants-platform/internal/ai/providers"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

type RetrieveOptions struct {
	TopK     int
	MinScore float64
}
type KnowledgeChunk struct {
	ID                uuid.UUID       `json:"chunk_id"`
	DocumentID        uuid.UUID       `json:"document_id"`
	Filename, Content string          `json:"-"`
	Score             float64         `json:"score"`
	Metadata          json.RawMessage `json:"metadata"`
}

func (k KnowledgeChunk) Source() map[string]any {
	var m map[string]any
	_ = json.Unmarshal(k.Metadata, &m)
	if m == nil {
		m = map[string]any{}
	}
	m["chunk_id"] = k.ID
	m["document_id"] = k.DocumentID
	m["filename"] = k.Filename
	m["score"] = k.Score
	return m
}

type KnowledgeRetriever interface {
	Retrieve(context.Context, uuid.UUID, uuid.UUID, string, RetrieveOptions) ([]KnowledgeChunk, error)
}
type Retriever struct {
	db *pgxpool.Pool
	p  providers.Provider
}

func NewRetriever(db *pgxpool.Pool, p providers.Provider) *Retriever { return &Retriever{db: db, p: p} }
func (r *Retriever) Retrieve(ctx context.Context, org, assistant uuid.UUID, q string, opt RetrieveOptions) ([]KnowledgeChunk, error) {
	if opt.TopK <= 0 {
		return nil, nil
	}
	v, err := r.p.Embed(ctx, []string{q})
	if err != nil {
		return nil, err
	}
	rows, err := r.db.Query(ctx, `SELECT dc.id,dc.document_id,d.original_filename,dc.content,1-(dc.embedding <=> $3) AS score,dc.metadata FROM document_chunks dc JOIN documents d ON d.id=dc.document_id AND d.organization_id=dc.organization_id JOIN assistant_knowledge_bases akb ON akb.organization_id=dc.organization_id AND akb.knowledge_base_id=dc.knowledge_base_id WHERE dc.organization_id=$1 AND akb.assistant_id=$2 AND d.status='ready' AND 1-(dc.embedding <=> $3) >= $4 ORDER BY dc.embedding <=> $3 LIMIT $5`, org, assistant, pgvector.NewVector(v[0]), opt.MinScore, opt.TopK)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []KnowledgeChunk
	for rows.Next() {
		var x KnowledgeChunk
		if err = rows.Scan(&x.ID, &x.DocumentID, &x.Filename, &x.Content, &x.Score, &x.Metadata); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
