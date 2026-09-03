package app

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/example/ai-assistants-platform/internal/ai"
	"github.com/example/ai-assistants-platform/internal/ai/providers"
	"github.com/example/ai-assistants-platform/internal/assistant"
	"github.com/example/ai-assistants-platform/internal/auth"
	"github.com/example/ai-assistants-platform/internal/conversation"
	"github.com/example/ai-assistants-platform/internal/dbgen"
	"github.com/example/ai-assistants-platform/internal/feedback"
	"github.com/example/ai-assistants-platform/internal/knowledge"
	"github.com/example/ai-assistants-platform/internal/platform/config"
	mw "github.com/example/ai-assistants-platform/internal/platform/middleware"
	"github.com/example/ai-assistants-platform/internal/platform/response"
	"github.com/example/ai-assistants-platform/internal/storage"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

type App struct {
	cfg           config.Config
	db            *pgxpool.Pool
	log           *slog.Logger
	auth          *auth.Service
	assistants    *assistant.Service
	knowledge     *knowledge.Service
	conversations *conversation.Service
	feedback      *feedback.Service
	processor     *knowledge.Processor
	router        http.Handler
}

func New(cfg config.Config, db *pgxpool.Pool, log *slog.Logger) (*App, error) {
	st, err := storage.NewLocal(cfg.UploadDir)
	if err != nil {
		return nil, err
	}
	reg := providers.NewRegistry()
	openai := providers.NewOpenAI(cfg.OpenAIKey, cfg.OpenAIBaseURL, cfg.EmbeddingModel, config.EmbeddingDimensions, cfg.LLMTimeout)
	reg.Register("openai", openai)
	a := &App{cfg: cfg, db: db, log: log}
	a.auth = auth.New(db, cfg.JWTSecret, cfg.AccessTTL, cfg.RefreshTTL, cfg.PublicRegistration)
	a.assistants = assistant.NewService(db, cfg.DefaultProvider, cfg.DefaultChatModel)
	a.knowledge = knowledge.NewService(db, st, cfg.MaxUploadBytes)
	a.feedback = feedback.NewService(db, openai, cfg.AutoApproveAdminFeedback)
	retriever := knowledge.NewRetriever(db, openai)
	engine := ai.NewEngine(db, a.assistants, retriever, a.feedback, reg, log)
	a.conversations = conversation.NewService(db, a.assistants, engine)
	a.processor = knowledge.NewProcessor(db, st, openai, knowledge.Chunker{Size: cfg.ChunkSize, Overlap: cfg.ChunkOverlap}, cfg.DocumentWorkers, log)
	a.knowledge.SetProcessor(a.processor)
	a.router = a.routes()
	return a, nil
}
func (a *App) Start(ctx context.Context) error {
	if err := a.auth.Bootstrap(ctx, a.cfg.BootstrapEmail, a.cfg.BootstrapPassword, a.cfg.BootstrapOrg); err != nil {
		return fmt.Errorf("bootstrap: %w", err)
	}
	if a.cfg.BootstrapEmail != "" {
		a.log.Info("bootstrap checked")
	}
	return a.processor.Start(ctx)
}
func (a *App) Stop()                 { a.processor.Stop() }
func (a *App) Handler() http.Handler { return a.router }
func (a *App) routes() http.Handler {
	r := chi.NewRouter()
	r.Use(mw.RequestID)
	r.Use(func(n http.Handler) http.Handler { return mw.Recover(a.log, n) })
	r.Use(func(n http.Handler) http.Handler { return mw.Logging(a.log, n) })
	r.Use(mw.SecureHeaders)
	r.Use(func(n http.Handler) http.Handler { return mw.CORS(a.cfg.CORS, n) })
	r.Use(mw.NewLimiter(a.cfg.RateRequests, a.cfg.RateWindow).Middleware)
	r.Get("/health", func(w http.ResponseWriter, r *http.Request) {
		response.JSON(w, 200, map[string]string{"status": "ok"}, nil)
	})
	r.Get("/ready", a.ready)
	r.Get("/openapi.yaml", a.openapi)
	r.Get("/docs", a.docs)
	r.Route("/api/v1", func(r chi.Router) {
		r.Route("/auth", func(r chi.Router) {
			r.Post("/register", a.register)
			r.Post("/login", a.login)
			r.Post("/refresh", a.refresh)
			r.Post("/logout", a.logout)
		})
		r.Group(func(r chi.Router) {
			r.Use(func(n http.Handler) http.Handler { return mw.Authenticate(a.auth, n) })
			r.Get("/me", a.me)
			r.Route("/users", func(r chi.Router) {
				r.Get("/", a.listUsers)
				r.Post("/", a.createUser)
				r.Get("/{id}", a.getUser)
				r.Patch("/{id}", a.updateUser)
				r.Delete("/{id}", a.deleteUser)
			})
			r.Route("/assistants", func(r chi.Router) {
				r.Get("/", a.listAssistants)
				r.Post("/", a.createAssistant)
				r.Get("/{id}", a.getAssistant)
				r.Patch("/{id}", a.updateAssistant)
				r.Delete("/{id}", a.deleteAssistant)
				r.Post("/{id}/knowledge-bases/{kbID}", a.attachKB)
				r.Delete("/{id}/knowledge-bases/{kbID}", a.detachKB)
				r.Get("/{id}/users", a.listAssistantUsers)
				r.Post("/{id}/users/{userID}", a.grantAssistant)
				r.Delete("/{id}/users/{userID}", a.revokeAssistant)
				r.Post("/{id}/conversations", a.createConversation)
			})
			r.Route("/knowledge-bases", func(r chi.Router) {
				r.Get("/", a.listKB)
				r.Post("/", a.createKB)
				r.Get("/{id}", a.getKB)
				r.Patch("/{id}", a.updateKB)
				r.Delete("/{id}", a.deleteKB)
				r.Get("/{id}/documents", a.listDocuments)
				r.Post("/{id}/documents", a.uploadDocument)
				r.Post("/{id}/texts", a.addText)
			})
			r.Route("/documents", func(r chi.Router) { r.Get("/{id}", a.getDocument); r.Delete("/{id}", a.deleteDocument) })
			r.Route("/conversations", func(r chi.Router) {
				r.Get("/", a.listConversations)
				r.Get("/{id}", a.getConversation)
				r.Patch("/{id}", a.renameConversation)
				r.Delete("/{id}", a.deleteConversation)
				r.With(mw.NewLimiter(a.cfg.ChatRateRequests, a.cfg.RateWindow).Middleware).Post("/{id}/messages", a.sendMessage)
			})
			r.Post("/messages/{id}/feedback", a.submitFeedback)
			r.Route("/feedback", func(r chi.Router) {
				r.Use(mw.RequireAdmin)
				r.Get("/", a.listFeedback)
				r.Get("/{id}", a.getFeedback)
				r.Patch("/{id}", a.moderateFeedback)
				r.Delete("/{id}", a.deleteFeedback)
			})
			r.With(mw.RequireAdmin).Get("/admin/stats", a.stats)
		})
	})
	return r
}
func (a *App) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if _, err := dbgen.New(a.db).Ping(ctx); err != nil {
		response.WriteError(w, response.E(503, "NOT_READY", "Database unavailable"))
		return
	}
	var v int64
	if err := a.db.QueryRow(ctx, `SELECT COALESCE(max(version_id),0) FROM goose_db_version WHERE is_applied`).Scan(&v); err != nil || v < 1 {
		response.WriteError(w, response.E(503, "NOT_READY", "Migrations unavailable"))
		return
	}
	response.JSON(w, 200, map[string]any{"status": "ready", "migration_version": v}, nil)
}
func actor(r *http.Request) auth.Actor { x, _ := mw.Actor(r.Context()); return x }
func idParam(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, name))
	if err != nil {
		return id, response.E(400, "INVALID_UUID", "Invalid UUID")
	}
	return id, nil
}
func page(r *http.Request) (int, int) {
	p, _ := strconv.Atoi(r.URL.Query().Get("page"))
	l, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if p < 1 {
		p = 1
	}
	if l < 1 {
		l = 20
	}
	if l > 100 {
		l = 100
	}
	return p, l
}
func optionalUUID(s string) (*uuid.UUID, error) {
	if s == "" {
		return nil, nil
	}
	id, err := uuid.Parse(s)
	if err != nil {
		return nil, response.E(400, "INVALID_UUID", "Invalid UUID filter")
	}
	return &id, nil
}
func ok(w http.ResponseWriter, data any)    { response.JSON(w, 200, data, nil) }
func fail(w http.ResponseWriter, err error) { response.WriteError(w, err) }

func (a *App) register(w http.ResponseWriter, r *http.Request) {
	var in struct {
		OrganizationName string `json:"organization_name"`
		Name             string `json:"name"`
		Email            string `json:"email"`
		Password         string `json:"password"`
	}
	if err := response.Decode(w, r, &in, 1<<20); err != nil {
		fail(w, err)
		return
	}
	u, t, err := a.auth.Register(r.Context(), in.OrganizationName, in.Name, in.Email, in.Password)
	if err != nil {
		fail(w, err)
		return
	}
	response.JSON(w, 201, map[string]any{"user": u, "tokens": t}, nil)
}
func (a *App) login(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email, Password string }
	if err := response.Decode(w, r, &in, 1<<20); err != nil {
		fail(w, err)
		return
	}
	u, t, err := a.auth.Authenticate(r.Context(), in.Email, in.Password)
	if err != nil {
		fail(w, err)
		return
	}
	ok(w, map[string]any{"user": u, "tokens": t})
}
func (a *App) refresh(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := response.Decode(w, r, &in, 1<<20); err != nil {
		fail(w, err)
		return
	}
	t, err := a.auth.Refresh(r.Context(), in.RefreshToken)
	if err != nil {
		fail(w, err)
		return
	}
	ok(w, t)
}
func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	var in struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := response.Decode(w, r, &in, 1<<20); err != nil {
		fail(w, err)
		return
	}
	if err := a.auth.Logout(r.Context(), in.RefreshToken); err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (a *App) me(w http.ResponseWriter, r *http.Request) {
	x, err := a.auth.LoadActor(r.Context(), actor(r))
	if err != nil {
		fail(w, err)
		return
	}
	ok(w, x)
}

func (a *App) listUsers(w http.ResponseWriter, r *http.Request) {
	p, l := page(r)
	x, n, e := a.auth.ListUsers(r.Context(), actor(r), p, l, r.URL.Query().Get("search"))
	if e != nil {
		fail(w, e)
		return
	}
	response.JSON(w, 200, x, map[string]any{"page": p, "limit": l, "total": n})
}
func (a *App) createUser(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name, Email, Password, Role string }
	if e := response.Decode(w, r, &in, 1<<20); e != nil {
		fail(w, e)
		return
	}
	x, e := a.auth.CreateUser(r.Context(), actor(r), in.Name, in.Email, in.Password, in.Role)
	if e != nil {
		fail(w, e)
		return
	}
	response.JSON(w, 201, x, nil)
}
func (a *App) getUser(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r, "id")
	if e != nil {
		fail(w, e)
		return
	}
	x, e := a.auth.GetUser(r.Context(), actor(r), id)
	if e != nil {
		fail(w, e)
		return
	}
	ok(w, x)
}
func (a *App) updateUser(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r, "id")
	if e != nil {
		fail(w, e)
		return
	}
	var in struct{ Name, Role, Status *string }
	if e = response.Decode(w, r, &in, 1<<20); e != nil {
		fail(w, e)
		return
	}
	x, e := a.auth.UpdateUser(r.Context(), actor(r), id, in.Name, in.Role, in.Status)
	if e != nil {
		fail(w, e)
		return
	}
	ok(w, x)
}
func (a *App) deleteUser(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r, "id")
	if e == nil {
		e = a.auth.DeactivateUser(r.Context(), actor(r), id)
	}
	if e != nil {
		fail(w, e)
		return
	}
	w.WriteHeader(204)
}

func (a *App) listAssistants(w http.ResponseWriter, r *http.Request) {
	p, l := page(r)
	x, n, e := a.assistants.List(r.Context(), actor(r), p, l, r.URL.Query().Get("search"))
	if e != nil {
		fail(w, e)
		return
	}
	response.JSON(w, 200, x, map[string]any{"page": p, "limit": l, "total": n})
}
func (a *App) createAssistant(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name                   string          `json:"name"`
		Slug                   string          `json:"slug"`
		Description            string          `json:"description"`
		SystemPrompt           string          `json:"system_prompt"`
		Provider               string          `json:"provider"`
		Model                  string          `json:"model"`
		Temperature            float32         `json:"temperature"`
		MaxOutputTokens        int             `json:"max_output_tokens"`
		Settings               json.RawMessage `json:"settings"`
		IsAvailableForAllUsers bool            `json:"is_available_for_all_users"`
	}
	if e := response.Decode(w, r, &in, 2<<20); e != nil {
		fail(w, e)
		return
	}
	x, e := a.assistants.Create(r.Context(), actor(r), assistant.CreateInput{Name: in.Name, Slug: in.Slug, Description: in.Description, SystemPrompt: in.SystemPrompt, Provider: in.Provider, Model: in.Model, Temperature: in.Temperature, MaxOutputTokens: in.MaxOutputTokens, Settings: in.Settings, AvailableAll: in.IsAvailableForAllUsers})
	if e != nil {
		fail(w, e)
		return
	}
	response.JSON(w, 201, x, nil)
}
func (a *App) getAssistant(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r, "id")
	if e != nil {
		fail(w, e)
		return
	}
	x, e := a.assistants.Get(r.Context(), actor(r), id)
	if e != nil {
		fail(w, e)
		return
	}
	ok(w, x)
}
func (a *App) updateAssistant(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r, "id")
	if e != nil {
		fail(w, e)
		return
	}
	var in struct {
		Name                   *string         `json:"name"`
		Description            *string         `json:"description"`
		SystemPrompt           *string         `json:"system_prompt"`
		Provider               *string         `json:"provider"`
		Model                  *string         `json:"model"`
		Temperature            *float32        `json:"temperature"`
		MaxOutputTokens        *int            `json:"max_output_tokens"`
		Settings               json.RawMessage `json:"settings"`
		IsAvailableForAllUsers *bool           `json:"is_available_for_all_users"`
	}
	if e = response.Decode(w, r, &in, 2<<20); e != nil {
		fail(w, e)
		return
	}
	x, e := a.assistants.Update(r.Context(), actor(r), id, assistant.UpdateInput{Name: in.Name, Description: in.Description, SystemPrompt: in.SystemPrompt, Provider: in.Provider, Model: in.Model, Temperature: in.Temperature, MaxOutputTokens: in.MaxOutputTokens, Settings: in.Settings, AvailableAll: in.IsAvailableForAllUsers})
	if e != nil {
		fail(w, e)
		return
	}
	ok(w, x)
}
func (a *App) deleteAssistant(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r, "id")
	if e == nil {
		e = a.assistants.Delete(r.Context(), actor(r), id)
	}
	if e != nil {
		fail(w, e)
		return
	}
	w.WriteHeader(204)
}
func (a *App) kbLink(w http.ResponseWriter, r *http.Request, attach bool) {
	id, e := idParam(r, "id")
	if e != nil {
		fail(w, e)
		return
	}
	kb, e := idParam(r, "kbID")
	if e == nil {
		e = a.assistants.AttachKB(r.Context(), actor(r), id, kb, attach)
	}
	if e != nil {
		fail(w, e)
		return
	}
	w.WriteHeader(204)
}
func (a *App) attachKB(w http.ResponseWriter, r *http.Request) { a.kbLink(w, r, true) }
func (a *App) detachKB(w http.ResponseWriter, r *http.Request) { a.kbLink(w, r, false) }
func (a *App) assistantUser(w http.ResponseWriter, r *http.Request, grant bool) {
	id, e := idParam(r, "id")
	if e != nil {
		fail(w, e)
		return
	}
	uid, e := idParam(r, "userID")
	if e == nil {
		e = a.assistants.SetUserAccess(r.Context(), actor(r), id, uid, grant)
	}
	if e != nil {
		fail(w, e)
		return
	}
	w.WriteHeader(204)
}
func (a *App) grantAssistant(w http.ResponseWriter, r *http.Request)  { a.assistantUser(w, r, true) }
func (a *App) revokeAssistant(w http.ResponseWriter, r *http.Request) { a.assistantUser(w, r, false) }
func (a *App) listAssistantUsers(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r, "id")
	if e != nil {
		fail(w, e)
		return
	}
	x, e := a.assistants.ListUsers(r.Context(), actor(r), id)
	if e != nil {
		fail(w, e)
		return
	}
	ok(w, x)
}

func (a *App) listKB(w http.ResponseWriter, r *http.Request) {
	p, l := page(r)
	x, n, e := a.knowledge.ListBases(r.Context(), actor(r), p, l, r.URL.Query().Get("search"))
	if e != nil {
		fail(w, e)
		return
	}
	response.JSON(w, 200, x, map[string]any{"page": p, "limit": l, "total": n})
}
func (a *App) createKB(w http.ResponseWriter, r *http.Request) {
	var in struct{ Name, Description string }
	if e := response.Decode(w, r, &in, 1<<20); e != nil {
		fail(w, e)
		return
	}
	x, e := a.knowledge.CreateBase(r.Context(), actor(r), in.Name, in.Description)
	if e != nil {
		fail(w, e)
		return
	}
	response.JSON(w, 201, x, nil)
}
func (a *App) getKB(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r, "id")
	if e != nil {
		fail(w, e)
		return
	}
	x, e := a.knowledge.GetBase(r.Context(), actor(r), id)
	if e != nil {
		fail(w, e)
		return
	}
	ok(w, x)
}
func (a *App) updateKB(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r, "id")
	if e != nil {
		fail(w, e)
		return
	}
	var in struct{ Name, Description *string }
	if e = response.Decode(w, r, &in, 1<<20); e != nil {
		fail(w, e)
		return
	}
	x, e := a.knowledge.UpdateBase(r.Context(), actor(r), id, in.Name, in.Description)
	if e != nil {
		fail(w, e)
		return
	}
	ok(w, x)
}
func (a *App) deleteKB(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r, "id")
	if e == nil {
		e = a.knowledge.DeleteBase(r.Context(), actor(r), id)
	}
	if e != nil {
		fail(w, e)
		return
	}
	w.WriteHeader(204)
}
func (a *App) listDocuments(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r, "id")
	if e != nil {
		fail(w, e)
		return
	}
	p, l := page(r)
	x, n, e := a.knowledge.ListDocuments(r.Context(), actor(r), id, p, l, r.URL.Query().Get("search"))
	if e != nil {
		fail(w, e)
		return
	}
	response.JSON(w, 200, x, map[string]any{"page": p, "limit": l, "total": n})
}
func (a *App) uploadDocument(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r, "id")
	if e != nil {
		fail(w, e)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, a.cfg.MaxUploadBytes+(1<<20))
	if e = r.ParseMultipartForm(a.cfg.MaxUploadBytes); e != nil {
		fail(w, response.E(422, "INVALID_UPLOAD", "Invalid or oversized multipart upload"))
		return
	}
	_, h, e := r.FormFile("file")
	if e != nil {
		fail(w, response.E(422, "FILE_REQUIRED", "Multipart field 'file' is required"))
		return
	}
	x, e := a.knowledge.Upload(r.Context(), actor(r), id, h)
	if e != nil {
		fail(w, e)
		return
	}
	response.JSON(w, 202, x, nil)
}

var _ *multipart.FileHeader

func (a *App) addText(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r, "id")
	if e != nil {
		fail(w, e)
		return
	}
	var in struct{ Title, Content string }
	if e = response.Decode(w, r, &in, a.cfg.MaxUploadBytes); e != nil {
		fail(w, e)
		return
	}
	x, e := a.knowledge.AddText(r.Context(), actor(r), id, in.Title, in.Content)
	if e != nil {
		fail(w, e)
		return
	}
	response.JSON(w, 202, x, nil)
}
func (a *App) getDocument(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r, "id")
	if e != nil {
		fail(w, e)
		return
	}
	x, e := a.knowledge.GetDocument(r.Context(), actor(r), id)
	if e != nil {
		fail(w, e)
		return
	}
	ok(w, x)
}
func (a *App) deleteDocument(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r, "id")
	if e == nil {
		e = a.knowledge.DeleteDocument(r.Context(), actor(r), id)
	}
	if e != nil {
		fail(w, e)
		return
	}
	w.WriteHeader(204)
}

func (a *App) createConversation(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r, "id")
	if e != nil {
		fail(w, e)
		return
	}
	var in struct{ Title string }
	if r.ContentLength > 0 {
		if e = response.Decode(w, r, &in, 1<<20); e != nil {
			fail(w, e)
			return
		}
	}
	x, e := a.conversations.Create(r.Context(), actor(r), id, in.Title)
	if e != nil {
		fail(w, e)
		return
	}
	response.JSON(w, 201, x, nil)
}
func (a *App) listConversations(w http.ResponseWriter, r *http.Request) {
	p, l := page(r)
	aid, e := optionalUUID(r.URL.Query().Get("assistant_id"))
	if e != nil {
		fail(w, e)
		return
	}
	x, n, e := a.conversations.List(r.Context(), actor(r), p, l, aid, r.URL.Query().Get("search"), r.URL.Query().Get("sort"))
	if e != nil {
		fail(w, e)
		return
	}
	response.JSON(w, 200, x, map[string]any{"page": p, "limit": l, "total": n})
}
func (a *App) getConversation(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r, "id")
	if e != nil {
		fail(w, e)
		return
	}
	x, e := a.conversations.Get(r.Context(), actor(r), id)
	if e != nil {
		fail(w, e)
		return
	}
	ok(w, x)
}
func (a *App) renameConversation(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r, "id")
	if e != nil {
		fail(w, e)
		return
	}
	var in struct{ Title string }
	if e = response.Decode(w, r, &in, 1<<20); e != nil {
		fail(w, e)
		return
	}
	x, e := a.conversations.Rename(r.Context(), actor(r), id, in.Title)
	if e != nil {
		fail(w, e)
		return
	}
	ok(w, x)
}
func (a *App) deleteConversation(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r, "id")
	if e == nil {
		e = a.conversations.Delete(r.Context(), actor(r), id)
	}
	if e != nil {
		fail(w, e)
		return
	}
	w.WriteHeader(204)
}
func (a *App) sendMessage(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r, "id")
	if e != nil {
		fail(w, e)
		return
	}
	var in struct{ Content string }
	if e = response.Decode(w, r, &in, 2<<20); e != nil {
		fail(w, e)
		return
	}
	events, e := a.conversations.Send(r.Context(), actor(r), id, in.Content)
	if e != nil {
		fail(w, e)
		return
	}
	fl, okf := w.(http.Flusher)
	if !okf {
		fail(w, response.E(500, "STREAMING_UNSUPPORTED", "Streaming is unavailable"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(200)
	fl.Flush()
	for ev := range events {
		b, _ := json.Marshal(ev.Data)
		_, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, b)
		fl.Flush()
		if ev.Type == "error" {
			return
		}
	}
}

func (a *App) submitFeedback(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r, "id")
	if e != nil {
		fail(w, e)
		return
	}
	var in struct {
		Rating          int     `json:"rating"`
		Comment         *string `json:"comment"`
		CorrectedAnswer *string `json:"corrected_answer"`
	}
	if e = response.Decode(w, r, &in, 2<<20); e != nil {
		fail(w, e)
		return
	}
	x, e := a.feedback.Submit(r.Context(), actor(r), id, in.Rating, in.Comment, in.CorrectedAnswer)
	if e != nil {
		fail(w, e)
		return
	}
	response.JSON(w, 201, x, nil)
}
func (a *App) listFeedback(w http.ResponseWriter, r *http.Request) {
	p, l := page(r)
	aid, e := optionalUUID(r.URL.Query().Get("assistant_id"))
	if e != nil {
		fail(w, e)
		return
	}
	uid, e := optionalUUID(r.URL.Query().Get("user_id"))
	if e != nil {
		fail(w, e)
		return
	}
	var rating *int
	if x := r.URL.Query().Get("rating"); x != "" {
		v, er := strconv.Atoi(x)
		if er != nil {
			fail(w, response.E(400, "INVALID_FILTER", "Invalid rating"))
			return
		}
		rating = &v
	}
	x, n, e := a.feedback.List(r.Context(), actor(r), p, l, aid, uid, rating, r.URL.Query().Get("status"))
	if e != nil {
		fail(w, e)
		return
	}
	response.JSON(w, 200, x, map[string]any{"page": p, "limit": l, "total": n})
}
func (a *App) getFeedback(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r, "id")
	if e != nil {
		fail(w, e)
		return
	}
	x, e := a.feedback.Get(r.Context(), actor(r), id)
	if e != nil {
		fail(w, e)
		return
	}
	ok(w, x)
}
func (a *App) moderateFeedback(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r, "id")
	if e != nil {
		fail(w, e)
		return
	}
	var in struct{ Status string }
	if e = response.Decode(w, r, &in, 1<<20); e != nil {
		fail(w, e)
		return
	}
	x, e := a.feedback.Moderate(r.Context(), actor(r), id, in.Status)
	if e != nil {
		fail(w, e)
		return
	}
	ok(w, x)
}
func (a *App) deleteFeedback(w http.ResponseWriter, r *http.Request) {
	id, e := idParam(r, "id")
	if e == nil {
		e = a.feedback.Delete(r.Context(), actor(r), id)
	}
	if e != nil {
		fail(w, e)
		return
	}
	w.WriteHeader(204)
}
func (a *App) stats(w http.ResponseWriter, r *http.Request) {
	org := actor(r).OrganizationID
	var users, assistants, conversations, messages, documents, pos, neg int
	err := a.db.QueryRow(r.Context(), `SELECT (SELECT count(*) FROM organization_users WHERE organization_id=$1),(SELECT count(*) FROM assistants WHERE organization_id=$1 AND status='active'),(SELECT count(*) FROM conversations WHERE organization_id=$1 AND status='active'),(SELECT count(*) FROM messages WHERE organization_id=$1),(SELECT count(*) FROM documents WHERE organization_id=$1),(SELECT count(*) FROM message_feedback WHERE organization_id=$1 AND rating=1),(SELECT count(*) FROM message_feedback WHERE organization_id=$1 AND rating=-1)`, org).Scan(&users, &assistants, &conversations, &messages, &documents, &pos, &neg)
	if err != nil {
		fail(w, err)
		return
	}
	ok(w, map[string]any{"users": users, "assistants": assistants, "conversations": conversations, "messages": messages, "documents": documents, "feedback": map[string]int{"positive": pos, "negative": neg}})
}
func (a *App) docs(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(`<!doctype html><html><head><title>AI Platform API</title><meta charset="utf-8"><meta name="viewport" content="width=device-width"><style>body{font:16px system-ui;max-width:800px;margin:4rem auto;padding:1rem}code{background:#eee;padding:.2rem}</style></head><body><h1>AI Assistants Platform API</h1><p>OpenAPI specification: <a href="/openapi.yaml"><code>/openapi.yaml</code></a></p><p>All business endpoints are rooted at <code>/api/v1</code>. Authenticate with a Bearer access token.</p></body></html>`))
}
func (a *App) openapi(w http.ResponseWriter, r *http.Request) {
	http.ServeFile(w, r, "docs/openapi.yaml")
}

var _ = strings.TrimSpace
