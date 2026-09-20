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
	candidateLimit := opt.TopK * 4
	if candidateLimit < 20 {
		candidateLimit = 20
	}
	rows, err := r.db.Query(ctx, `WITH lexical_query AS (SELECT websearch_to_tsquery('simple',$4) AS value), candidates AS (SELECT dc.id,dc.document_id,d.original_filename,dc.content,dc.metadata,1-(dc.embedding <=> $3) AS vector_score,0::real AS lexical_score FROM document_chunks dc JOIN documents d ON d.id=dc.document_id AND d.organization_id=dc.organization_id JOIN assistant_knowledge_bases akb ON akb.organization_id=dc.organization_id AND akb.knowledge_base_id=dc.knowledge_base_id WHERE dc.organization_id=$1 AND akb.assistant_id=$2 AND d.status='ready' ORDER BY dc.embedding <=> $3 LIMIT $6) , lexical_candidates AS (SELECT dc.id,dc.document_id,d.original_filename,dc.content,dc.metadata,1-(dc.embedding <=> $3) AS vector_score,LEAST(ts_rank_cd(to_tsvector('simple',dc.content),lq.value),1)::real AS lexical_score FROM document_chunks dc JOIN documents d ON d.id=dc.document_id AND d.organization_id=dc.organization_id JOIN assistant_knowledge_bases akb ON akb.organization_id=dc.organization_id AND akb.knowledge_base_id=dc.knowledge_base_id CROSS JOIN lexical_query lq WHERE dc.organization_id=$1 AND akb.assistant_id=$2 AND d.status='ready' AND lq.value @@ to_tsvector('simple',dc.content) ORDER BY lexical_score DESC LIMIT $6), ranked AS (SELECT id,document_id,original_filename,content,metadata,max(vector_score) vector_score,max(lexical_score) lexical_score FROM (SELECT * FROM candidates UNION ALL SELECT * FROM lexical_candidates) all_candidates GROUP BY id,document_id,original_filename,content,metadata) SELECT id,document_id,original_filename,content,(0.8*vector_score+0.2*lexical_score) AS score,metadata FROM ranked WHERE vector_score >= $5 OR lexical_score > 0 ORDER BY score DESC LIMIT $7`, org, assistant, pgvector.NewVector(v[0]), q, opt.MinScore, candidateLimit, opt.TopK)
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
