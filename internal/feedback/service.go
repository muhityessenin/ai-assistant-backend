package feedback

import (
	"context"
	"encoding/json"
	"errors"
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

type Feedback struct {
	ID, AssistantID, ConversationID, MessageID, UserID uuid.UUID
	Rating                                             int16
	Comment, CorrectedAnswer                           *string
	Status, ConversationTitle, MessageContent          string
	CreatedAt, UpdatedAt                               time.Time
}

func (f Feedback) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"id": f.ID, "assistant_id": f.AssistantID, "conversation_id": f.ConversationID, "conversation_title": f.ConversationTitle, "message_id": f.MessageID, "message_content": f.MessageContent, "user_id": f.UserID, "rating": f.Rating, "comment": f.Comment, "corrected_answer": f.CorrectedAnswer, "status": f.Status, "created_at": f.CreatedAt, "updated_at": f.UpdatedAt})
}

type Example struct {
	Input, Original, Corrected string
	Score                      float64
}
type Service struct {
	db          *pgxpool.Pool
	p           providers.Provider
	autoApprove bool
}

func NewService(db *pgxpool.Pool, p providers.Provider, auto bool) *Service {
	return &Service{db: db, p: p, autoApprove: auto}
}
func (s *Service) Submit(ctx context.Context, a auth.Actor, messageID uuid.UUID, rating int, comment, corrected *string) (Feedback, error) {
	if rating < -1 || rating > 1 {
		return Feedback{}, response.E(422, "VALIDATION_ERROR", "rating must be -1, 0 or 1")
	}
	var assistantID, convID uuid.UUID
	var original, input, conversationTitle string
	err := s.db.QueryRow(ctx, `SELECT c.assistant_id,c.id,c.title,m.content,(SELECT um.content FROM messages um WHERE um.organization_id=m.organization_id AND um.conversation_id=m.conversation_id AND um.role='user' AND um.created_at<m.created_at ORDER BY um.created_at DESC LIMIT 1) FROM messages m JOIN conversations c ON c.organization_id=m.organization_id AND c.id=m.conversation_id WHERE m.organization_id=$1 AND m.id=$2 AND m.role='assistant' AND ($3 OR c.user_id=$4)`, a.OrganizationID, messageID, a.IsAdmin(), a.UserID).Scan(&assistantID, &convID, &conversationTitle, &original, &input)
	if errors.Is(err, pgx.ErrNoRows) {
		return Feedback{}, response.ErrNotFound
	}
	if err != nil {
		return Feedback{}, err
	}
	status := "pending"
	if s.autoApprove && a.IsAdmin() {
		status = "approved"
	}
	var correctionVector *pgvector.Vector
	if corrected != nil && strings.TrimSpace(*corrected) != "" {
		emb, embedErr := s.p.Embed(ctx, []string{input + "\nCorrection context: " + value(comment)})
		if embedErr != nil {
			return Feedback{}, embedErr
		}
		v := pgvector.NewVector(emb[0])
		correctionVector = &v
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Feedback{}, err
	}
	defer tx.Rollback(ctx)
	var f Feedback
	err = tx.QueryRow(ctx, `INSERT INTO message_feedback(organization_id,assistant_id,conversation_id,message_id,user_id,rating,comment,corrected_answer,status) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT(message_id,user_id) DO UPDATE SET rating=EXCLUDED.rating,comment=EXCLUDED.comment,corrected_answer=EXCLUDED.corrected_answer,status=EXCLUDED.status,updated_at=now() RETURNING id,assistant_id,conversation_id,message_id,user_id,rating,comment,corrected_answer,status,created_at,updated_at`, a.OrganizationID, assistantID, convID, messageID, a.UserID, rating, comment, corrected, status).Scan(&f.ID, &f.AssistantID, &f.ConversationID, &f.MessageID, &f.UserID, &f.Rating, &f.Comment, &f.CorrectedAnswer, &f.Status, &f.CreatedAt, &f.UpdatedAt)
	if err != nil {
		return f, err
	}
	if correctionVector != nil {
		_, err = tx.Exec(ctx, `INSERT INTO feedback_examples(organization_id,assistant_id,source_message_id,input_text,original_answer,corrected_answer,embedding,status) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(source_message_id) DO UPDATE SET input_text=EXCLUDED.input_text,original_answer=EXCLUDED.original_answer,corrected_answer=EXCLUDED.corrected_answer,embedding=EXCLUDED.embedding,status=EXCLUDED.status,updated_at=now()`, a.OrganizationID, assistantID, messageID, input, original, strings.TrimSpace(*corrected), *correctionVector, status)
		if err != nil {
			return f, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return f, err
	}
	f.ConversationTitle = conversationTitle
	f.MessageContent = original
	return f, nil
}
func (s *Service) Retrieve(ctx context.Context, org, assistant uuid.UUID, q string, top int) ([]Example, error) {
	if top <= 0 {
		return nil, nil
	}
	emb, err := s.p.Embed(ctx, []string{q})
	if err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx, `SELECT input_text,original_answer,corrected_answer,1-(embedding <=> $3) score FROM feedback_examples WHERE organization_id=$1 AND assistant_id=$2 AND status='approved' ORDER BY embedding <=> $3 LIMIT $4`, org, assistant, pgvector.NewVector(emb[0]), top)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Example
	for rows.Next() {
		var x Example
		if err = rows.Scan(&x.Input, &x.Original, &x.Corrected, &x.Score); err != nil {
			return nil, err
		}
		out = append(out, x)
	}
	return out, rows.Err()
}
func (s *Service) List(ctx context.Context, a auth.Actor, page, limit int, assistantID, userID *uuid.UUID, rating *int, status string) ([]Feedback, int, error) {
	if !a.IsAdmin() {
		return nil, 0, response.ErrForbidden
	}
	rows, err := s.db.Query(ctx, `SELECT f.id,f.assistant_id,f.conversation_id,c.title,f.message_id,m.content,f.user_id,f.rating,f.comment,f.corrected_answer,f.status,f.created_at,f.updated_at,count(*) OVER() FROM message_feedback f JOIN conversations c ON c.organization_id=f.organization_id AND c.id=f.conversation_id JOIN messages m ON m.organization_id=f.organization_id AND m.id=f.message_id WHERE f.organization_id=$1 AND ($2::uuid IS NULL OR f.assistant_id=$2) AND ($3::uuid IS NULL OR f.user_id=$3) AND ($4::smallint IS NULL OR f.rating=$4) AND ($5='' OR f.status::text=$5) ORDER BY f.created_at DESC LIMIT $6 OFFSET $7`, a.OrganizationID, assistantID, userID, rating, status, limit, (page-1)*limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Feedback
	total := 0
	for rows.Next() {
		var f Feedback
		if err = rows.Scan(&f.ID, &f.AssistantID, &f.ConversationID, &f.ConversationTitle, &f.MessageID, &f.MessageContent, &f.UserID, &f.Rating, &f.Comment, &f.CorrectedAnswer, &f.Status, &f.CreatedAt, &f.UpdatedAt, &total); err != nil {
			return nil, 0, err
		}
		out = append(out, f)
	}
	return out, total, rows.Err()
}
func (s *Service) Get(ctx context.Context, a auth.Actor, id uuid.UUID) (Feedback, error) {
	if !a.IsAdmin() {
		return Feedback{}, response.ErrForbidden
	}
	var f Feedback
	err := s.db.QueryRow(ctx, `SELECT f.id,f.assistant_id,f.conversation_id,c.title,f.message_id,m.content,f.user_id,f.rating,f.comment,f.corrected_answer,f.status,f.created_at,f.updated_at FROM message_feedback f JOIN conversations c ON c.organization_id=f.organization_id AND c.id=f.conversation_id JOIN messages m ON m.organization_id=f.organization_id AND m.id=f.message_id WHERE f.organization_id=$1 AND f.id=$2`, a.OrganizationID, id).Scan(&f.ID, &f.AssistantID, &f.ConversationID, &f.ConversationTitle, &f.MessageID, &f.MessageContent, &f.UserID, &f.Rating, &f.Comment, &f.CorrectedAnswer, &f.Status, &f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = response.ErrNotFound
	}
	return f, err
}
func (s *Service) Moderate(ctx context.Context, a auth.Actor, id uuid.UUID, status string) (Feedback, error) {
	if !a.IsAdmin() {
		return Feedback{}, response.ErrForbidden
	}
	if status != "approved" && status != "rejected" && status != "pending" {
		return Feedback{}, response.E(422, "VALIDATION_ERROR", "Invalid feedback status")
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Feedback{}, err
	}
	defer tx.Rollback(ctx)
	var f Feedback
	err = tx.QueryRow(ctx, `WITH updated AS (UPDATE message_feedback SET status=$3,updated_at=now() WHERE organization_id=$1 AND id=$2 RETURNING *) SELECT f.id,f.assistant_id,f.conversation_id,c.title,f.message_id,m.content,f.user_id,f.rating,f.comment,f.corrected_answer,f.status,f.created_at,f.updated_at FROM updated f JOIN conversations c ON c.organization_id=f.organization_id AND c.id=f.conversation_id JOIN messages m ON m.organization_id=f.organization_id AND m.id=f.message_id`, a.OrganizationID, id, status).Scan(&f.ID, &f.AssistantID, &f.ConversationID, &f.ConversationTitle, &f.MessageID, &f.MessageContent, &f.UserID, &f.Rating, &f.Comment, &f.CorrectedAnswer, &f.Status, &f.CreatedAt, &f.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return f, response.ErrNotFound
	}
	if err != nil {
		return f, err
	}
	_, err = tx.Exec(ctx, `UPDATE feedback_examples SET status=$3,updated_at=now() WHERE organization_id=$1 AND source_message_id=$2`, a.OrganizationID, f.MessageID, status)
	if err != nil {
		return f, err
	}
	return f, tx.Commit(ctx)
}
func (s *Service) Delete(ctx context.Context, a auth.Actor, id uuid.UUID) error {
	if !a.IsAdmin() {
		return response.ErrForbidden
	}
	tag, err := s.db.Exec(ctx, `DELETE FROM message_feedback WHERE organization_id=$1 AND id=$2`, a.OrganizationID, id)
	if err == nil && tag.RowsAffected() == 0 {
		return response.ErrNotFound
	}
	return err
}
func value(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
