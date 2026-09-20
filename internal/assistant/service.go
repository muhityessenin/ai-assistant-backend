package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/example/ai-assistants-platform/internal/auth"
	"github.com/example/ai-assistants-platform/internal/platform/response"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var defaultSettings = json.RawMessage(`{"rag":{"enabled":true,"top_k":8,"min_score":0.6},"feedback":{"enabled":true,"top_k":3},"history":{"message_limit":16},"citations":true}`)

type Settings struct {
	RAG struct {
		Enabled  bool    `json:"enabled"`
		TopK     int     `json:"top_k"`
		MinScore float64 `json:"min_score"`
	} `json:"rag"`
	Feedback struct {
		Enabled bool `json:"enabled"`
		TopK    int  `json:"top_k"`
	} `json:"feedback"`
	History struct {
		MessageLimit int `json:"message_limit"`
	} `json:"history"`
	Citations bool `json:"citations"`
}
type Assistant struct {
	ID                                                             uuid.UUID `json:"id"`
	Name, Slug, Description, SystemPrompt, Provider, Model, Status string
	Temperature                                                    float32
	MaxOutputTokens                                                int
	Settings                                                       json.RawMessage
	AvailableAll                                                   bool
	CreatedBy                                                      uuid.UUID
	CreatedAt, UpdatedAt                                           time.Time
}

func (a Assistant) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"id": a.ID, "name": a.Name, "slug": a.Slug, "description": a.Description, "system_prompt": a.SystemPrompt, "provider": a.Provider, "model": a.Model, "temperature": a.Temperature, "max_output_tokens": a.MaxOutputTokens, "settings": json.RawMessage(a.Settings), "is_available_for_all_users": a.AvailableAll, "status": a.Status, "created_by": a.CreatedBy, "created_at": a.CreatedAt, "updated_at": a.UpdatedAt})
}
func (a Assistant) ParsedSettings() Settings {
	var s Settings
	_ = json.Unmarshal(a.Settings, &s)
	if s.RAG.TopK <= 0 {
		s.RAG.TopK = 8
	}
	if s.RAG.TopK > 50 {
		s.RAG.TopK = 50
	}
	if s.RAG.MinScore < 0 {
		s.RAG.MinScore = 0
	}
	if s.RAG.MinScore > 1 {
		s.RAG.MinScore = 1
	}
	if s.History.MessageLimit <= 0 {
		s.History.MessageLimit = 16
	}
	if s.History.MessageLimit > 100 {
		s.History.MessageLimit = 100
	}
	if s.Feedback.TopK <= 0 {
		s.Feedback.TopK = 3
	}
	if s.Feedback.TopK > 20 {
		s.Feedback.TopK = 20
	}
	return s
}
func (a Assistant) Redacted() Assistant {
	a.SystemPrompt = ""
	a.Settings = json.RawMessage(`{}`)
	return a
}

type CreateInput struct {
	Name, Slug, Description, SystemPrompt, Provider, Model string
	Temperature                                            float32
	MaxOutputTokens                                        int
	Settings                                               json.RawMessage
	AvailableAll                                           bool
}
type UpdateInput struct {
	Name, Description, SystemPrompt, Provider, Model *string
	Temperature                                      *float32
	MaxOutputTokens                                  *int
	Settings                                         json.RawMessage
	AvailableAll                                     *bool
}
type Service struct {
	db                            *pgxpool.Pool
	defaultProvider, defaultModel string
}

type Publication struct {
	ID             uuid.UUID `json:"id"`
	OrganizationID uuid.UUID `json:"-"`
	AssistantID    uuid.UUID `json:"assistant_id"`
	AssistantName  string    `json:"assistant_name"`
	Description    string    `json:"description"`
	Enabled        bool      `json:"enabled"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
}

func NewService(db *pgxpool.Pool, p, m string) *Service {
	return &Service{db: db, defaultProvider: p, defaultModel: m}
}
func (s *Service) Create(ctx context.Context, a auth.Actor, in CreateInput) (Assistant, error) {
	if !a.IsAdmin() {
		return Assistant{}, response.ErrForbidden
	}
	in.Name = strings.TrimSpace(in.Name)
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	in.SystemPrompt = strings.TrimSpace(in.SystemPrompt)
	if in.Name == "" || in.SystemPrompt == "" || !regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`).MatchString(in.Slug) {
		return Assistant{}, response.E(422, "VALIDATION_ERROR", "Name, valid slug and system_prompt are required")
	}
	if in.Provider == "" {
		in.Provider = s.defaultProvider
	}
	if in.Provider != s.defaultProvider {
		return Assistant{}, response.E(422, "UNSUPPORTED_PROVIDER", "AI provider is not configured on this backend")
	}
	if in.Model == "" {
		in.Model = s.defaultModel
	}
	if in.MaxOutputTokens == 0 {
		in.MaxOutputTokens = 2048
	}
	if in.Temperature < 0 || in.Temperature > 2 || in.MaxOutputTokens < 1 || in.MaxOutputTokens > 32768 {
		return Assistant{}, response.E(422, "VALIDATION_ERROR", "Invalid model parameters")
	}
	if len(in.Settings) == 0 {
		in.Settings = defaultSettings
	}
	if !json.Valid(in.Settings) {
		return Assistant{}, response.E(422, "VALIDATION_ERROR", "settings must be valid JSON")
	}
	var x Assistant
	err := scan(s.db.QueryRow(ctx, `INSERT INTO assistants(organization_id,name,slug,description,system_prompt,provider,model,temperature,max_output_tokens,settings,is_available_for_all_users,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12) RETURNING id,name,slug,description,system_prompt,provider,model,temperature,max_output_tokens,settings,is_available_for_all_users,status,created_by,created_at,updated_at`, a.OrganizationID, in.Name, in.Slug, in.Description, in.SystemPrompt, in.Provider, in.Model, in.Temperature, in.MaxOutputTokens, in.Settings, in.AvailableAll, a.UserID), &x)
	return x, err
}
func (s *Service) Get(ctx context.Context, a auth.Actor, id uuid.UUID) (Assistant, error) {
	var x Assistant
	err := scan(s.db.QueryRow(ctx, `SELECT a.id,a.name,a.slug,a.description,a.system_prompt,a.provider,a.model,a.temperature,a.max_output_tokens,a.settings,a.is_available_for_all_users,a.status,a.created_by,a.created_at,a.updated_at FROM assistants a WHERE a.organization_id=$1 AND a.id=$2 AND a.status='active' AND ($3 OR a.is_available_for_all_users OR EXISTS(SELECT 1 FROM assistant_access aa WHERE aa.organization_id=$1 AND aa.assistant_id=a.id AND aa.user_id=$4))`, a.OrganizationID, id, a.IsAdmin(), a.UserID), &x)
	if errors.Is(err, pgx.ErrNoRows) {
		err = response.ErrNotFound
	}
	return x, err
}
func (s *Service) List(ctx context.Context, a auth.Actor, page, limit int, search string) ([]Assistant, int, error) {
	rows, err := s.db.Query(ctx, `SELECT a.id,a.name,a.slug,a.description,a.system_prompt,a.provider,a.model,a.temperature,a.max_output_tokens,a.settings,a.is_available_for_all_users,a.status,a.created_by,a.created_at,a.updated_at,count(*) OVER() FROM assistants a WHERE a.organization_id=$1 AND a.status='active' AND ($2 OR a.is_available_for_all_users OR EXISTS(SELECT 1 FROM assistant_access aa WHERE aa.organization_id=$1 AND aa.assistant_id=a.id AND aa.user_id=$3)) AND ($4='' OR a.name ILIKE '%'||$4||'%' OR a.description ILIKE '%'||$4||'%') ORDER BY a.updated_at DESC LIMIT $5 OFFSET $6`, a.OrganizationID, a.IsAdmin(), a.UserID, search, limit, (page-1)*limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Assistant
	total := 0
	for rows.Next() {
		var x Assistant
		if err = rows.Scan(&x.ID, &x.Name, &x.Slug, &x.Description, &x.SystemPrompt, &x.Provider, &x.Model, &x.Temperature, &x.MaxOutputTokens, &x.Settings, &x.AvailableAll, &x.Status, &x.CreatedBy, &x.CreatedAt, &x.UpdatedAt, &total); err != nil {
			return nil, 0, err
		}
		out = append(out, x)
	}
	return out, total, rows.Err()
}
func (s *Service) Update(ctx context.Context, a auth.Actor, id uuid.UUID, in UpdateInput) (Assistant, error) {
	if !a.IsAdmin() {
		return Assistant{}, response.ErrForbidden
	}
	if in.Temperature != nil && (*in.Temperature < 0 || *in.Temperature > 2) {
		return Assistant{}, response.E(422, "VALIDATION_ERROR", "temperature must be between 0 and 2")
	}
	if in.Provider != nil && *in.Provider != s.defaultProvider {
		return Assistant{}, response.E(422, "UNSUPPORTED_PROVIDER", "AI provider is not configured on this backend")
	}
	if in.Model != nil && strings.TrimSpace(*in.Model) == "" {
		return Assistant{}, response.E(422, "VALIDATION_ERROR", "model cannot be empty")
	}
	if len(in.Settings) > 0 && !json.Valid(in.Settings) {
		return Assistant{}, response.E(422, "VALIDATION_ERROR", "settings must be JSON")
	}
	var x Assistant
	err := scan(s.db.QueryRow(ctx, `UPDATE assistants SET name=COALESCE($3,name),description=COALESCE($4,description),system_prompt=COALESCE($5,system_prompt),provider=COALESCE($6,provider),model=COALESCE($7,model),temperature=COALESCE($8,temperature),max_output_tokens=COALESCE($9,max_output_tokens),settings=COALESCE($10,settings),is_available_for_all_users=COALESCE($11,is_available_for_all_users),updated_at=now() WHERE organization_id=$1 AND id=$2 AND status='active' RETURNING id,name,slug,description,system_prompt,provider,model,temperature,max_output_tokens,settings,is_available_for_all_users,status,created_by,created_at,updated_at`, a.OrganizationID, id, in.Name, in.Description, in.SystemPrompt, in.Provider, in.Model, in.Temperature, in.MaxOutputTokens, nullJSON(in.Settings), in.AvailableAll), &x)
	if errors.Is(err, pgx.ErrNoRows) {
		err = response.ErrNotFound
	}
	return x, err
}
func (s *Service) Delete(ctx context.Context, a auth.Actor, id uuid.UUID) error {
	if !a.IsAdmin() {
		return response.ErrForbidden
	}
	tag, err := s.db.Exec(ctx, `UPDATE assistants SET status='archived',updated_at=now() WHERE organization_id=$1 AND id=$2 AND status='active'`, a.OrganizationID, id)
	if err == nil && tag.RowsAffected() == 0 {
		return response.ErrNotFound
	}
	return err
}
func (s *Service) AttachKB(ctx context.Context, a auth.Actor, assistantID, kbID uuid.UUID, attach bool) error {
	if !a.IsAdmin() {
		return response.ErrForbidden
	}
	var affected int64
	if attach {
		t, err := s.db.Exec(ctx, `INSERT INTO assistant_knowledge_bases(organization_id,assistant_id,knowledge_base_id) SELECT $1,$2,$3 WHERE EXISTS(SELECT 1 FROM assistants WHERE organization_id=$1 AND id=$2 AND status='active') AND EXISTS(SELECT 1 FROM knowledge_bases WHERE organization_id=$1 AND id=$3 AND status='active') ON CONFLICT DO NOTHING`, a.OrganizationID, assistantID, kbID)
		if err != nil {
			return err
		}
		affected = t.RowsAffected()
	} else {
		t, err := s.db.Exec(ctx, `DELETE FROM assistant_knowledge_bases WHERE organization_id=$1 AND assistant_id=$2 AND knowledge_base_id=$3`, a.OrganizationID, assistantID, kbID)
		if err != nil {
			return err
		}
		affected = t.RowsAffected()
	}
	if affected == 0 {
		return response.ErrNotFound
	}
	return nil
}
func (s *Service) ListKBs(ctx context.Context, a auth.Actor, assistantID uuid.UUID) ([]map[string]any, error) {
	if !a.IsAdmin() {
		return nil, response.ErrForbidden
	}
	rows, err := s.db.Query(ctx, `SELECT kb.id,kb.name,kb.description,kb.status,kb.created_at,kb.updated_at FROM assistant_knowledge_bases akb JOIN assistants ast ON ast.organization_id=akb.organization_id AND ast.id=akb.assistant_id JOIN knowledge_bases kb ON kb.organization_id=akb.organization_id AND kb.id=akb.knowledge_base_id WHERE akb.organization_id=$1 AND akb.assistant_id=$2 AND ast.status='active' AND kb.status='active' ORDER BY kb.name`, a.OrganizationID, assistantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]map[string]any, 0)
	for rows.Next() {
		var id uuid.UUID
		var name, status string
		var description *string
		var createdAt, updatedAt time.Time
		if err = rows.Scan(&id, &name, &description, &status, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "name": name, "description": description, "status": status, "created_at": createdAt, "updated_at": updatedAt})
	}
	return out, rows.Err()
}
func (s *Service) Publish(ctx context.Context, a auth.Actor, assistantID uuid.UUID) (Publication, error) {
	if !a.IsAdmin() {
		return Publication{}, response.ErrForbidden
	}
	var x Publication
	err := s.db.QueryRow(ctx, `INSERT INTO assistant_publications(organization_id,assistant_id,created_by) SELECT $1,$2,$3 WHERE EXISTS(SELECT 1 FROM assistants WHERE organization_id=$1 AND id=$2 AND status='active') ON CONFLICT(organization_id,assistant_id) DO UPDATE SET enabled=true,updated_at=now() RETURNING id,organization_id,assistant_id,enabled,created_at,updated_at`, a.OrganizationID, assistantID, a.UserID).Scan(&x.ID, &x.OrganizationID, &x.AssistantID, &x.Enabled, &x.CreatedAt, &x.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Publication{}, response.ErrNotFound
	}
	return x, err
}
func (s *Service) GetPublication(ctx context.Context, id uuid.UUID) (Publication, error) {
	var x Publication
	err := s.db.QueryRow(ctx, `SELECT ap.id,ap.organization_id,ap.assistant_id,a.name,a.description,ap.enabled,ap.created_at,ap.updated_at FROM assistant_publications ap JOIN assistants a ON a.organization_id=ap.organization_id AND a.id=ap.assistant_id WHERE ap.id=$1 AND ap.enabled AND a.status='active'`, id).Scan(&x.ID, &x.OrganizationID, &x.AssistantID, &x.AssistantName, &x.Description, &x.Enabled, &x.CreatedAt, &x.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Publication{}, response.ErrNotFound
	}
	return x, err
}
func (s *Service) GetAssistantPublication(ctx context.Context, a auth.Actor, assistantID uuid.UUID) (Publication, error) {
	if !a.IsAdmin() {
		return Publication{}, response.ErrForbidden
	}
	var x Publication
	err := s.db.QueryRow(ctx, `SELECT ap.id,ap.organization_id,ap.assistant_id,ast.name,ast.description,ap.enabled,ap.created_at,ap.updated_at FROM assistant_publications ap JOIN assistants ast ON ast.organization_id=ap.organization_id AND ast.id=ap.assistant_id WHERE ap.organization_id=$1 AND ap.assistant_id=$2`, a.OrganizationID, assistantID).Scan(&x.ID, &x.OrganizationID, &x.AssistantID, &x.AssistantName, &x.Description, &x.Enabled, &x.CreatedAt, &x.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Publication{}, response.ErrNotFound
	}
	return x, err
}
func (s *Service) Unpublish(ctx context.Context, a auth.Actor, assistantID uuid.UUID) error {
	if !a.IsAdmin() {
		return response.ErrForbidden
	}
	tag, err := s.db.Exec(ctx, `UPDATE assistant_publications SET enabled=false,updated_at=now() WHERE organization_id=$1 AND assistant_id=$2 AND enabled`, a.OrganizationID, assistantID)
	if err == nil && tag.RowsAffected() == 0 {
		return response.ErrNotFound
	}
	return err
}
func (s *Service) SetUserAccess(ctx context.Context, a auth.Actor, assistantID, userID uuid.UUID, grant bool) error {
	if !a.IsAdmin() {
		return response.ErrForbidden
	}
	var affected int64
	if grant {
		t, err := s.db.Exec(ctx, `INSERT INTO assistant_access(organization_id,assistant_id,user_id) SELECT $1,$2,$3 WHERE EXISTS(SELECT 1 FROM assistants WHERE organization_id=$1 AND id=$2 AND status='active') AND EXISTS(SELECT 1 FROM organization_users WHERE organization_id=$1 AND user_id=$3) ON CONFLICT DO NOTHING`, a.OrganizationID, assistantID, userID)
		if err != nil {
			return err
		}
		affected = t.RowsAffected()
	} else {
		t, err := s.db.Exec(ctx, `DELETE FROM assistant_access WHERE organization_id=$1 AND assistant_id=$2 AND user_id=$3`, a.OrganizationID, assistantID, userID)
		if err != nil {
			return err
		}
		affected = t.RowsAffected()
	}
	if affected == 0 {
		return response.ErrNotFound
	}
	return nil
}
func (s *Service) ListUsers(ctx context.Context, a auth.Actor, id uuid.UUID) ([]map[string]any, error) {
	if !a.IsAdmin() {
		return nil, response.ErrForbidden
	}
	rows, err := s.db.Query(ctx, `SELECT u.id,u.email,u.name FROM assistant_access aa JOIN users u ON u.id=aa.user_id WHERE aa.organization_id=$1 AND aa.assistant_id=$2 ORDER BY u.name`, a.OrganizationID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id uuid.UUID
		var email, name string
		if err = rows.Scan(&id, &email, &name); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "email": email, "name": name})
	}
	return out, rows.Err()
}

type row interface{ Scan(...any) error }

func scan(r row, x *Assistant) error {
	return r.Scan(&x.ID, &x.Name, &x.Slug, &x.Description, &x.SystemPrompt, &x.Provider, &x.Model, &x.Temperature, &x.MaxOutputTokens, &x.Settings, &x.AvailableAll, &x.Status, &x.CreatedBy, &x.CreatedAt, &x.UpdatedAt)
}
func nullJSON(v json.RawMessage) any {
	if len(v) == 0 {
		return nil
	}
	return v
}
