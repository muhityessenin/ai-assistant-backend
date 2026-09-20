package knowledge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/example/ai-assistants-platform/internal/ai/providers"
	"github.com/example/ai-assistants-platform/internal/auth"
	"github.com/example/ai-assistants-platform/internal/platform/response"
	"github.com/example/ai-assistants-platform/internal/storage"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pgvector/pgvector-go"
)

type KnowledgeBase struct {
	ID          uuid.UUID `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}
type Document struct {
	ID, KnowledgeBaseID                          uuid.UUID `json:"-"`
	Filename, OriginalFilename, MIMEType, Status string
	SizeBytes                                    int64
	ErrorMessage                                 *string
	Metadata                                     json.RawMessage
	CreatedAt, UpdatedAt                         time.Time
}

func (d Document) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]any{"id": d.ID, "knowledge_base_id": d.KnowledgeBaseID, "filename": d.Filename, "original_filename": d.OriginalFilename, "mime_type": d.MIMEType, "size_bytes": d.SizeBytes, "status": d.Status, "error_message": d.ErrorMessage, "metadata": json.RawMessage(d.Metadata), "created_at": d.CreatedAt, "updated_at": d.UpdatedAt})
}

type Service struct {
	db        *pgxpool.Pool
	storage   storage.Storage
	processor *Processor
	maxBytes  int64
}

func NewService(db *pgxpool.Pool, st storage.Storage, max int64) *Service {
	return &Service{db: db, storage: st, maxBytes: max}
}
func (s *Service) SetProcessor(p *Processor) { s.processor = p }
func (s *Service) CreateBase(ctx context.Context, a auth.Actor, name, desc string) (KnowledgeBase, error) {
	if !a.IsAdmin() {
		return KnowledgeBase{}, response.ErrForbidden
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return KnowledgeBase{}, response.E(422, "VALIDATION_ERROR", "Name is required")
	}
	var v KnowledgeBase
	err := s.db.QueryRow(ctx, `INSERT INTO knowledge_bases(organization_id,name,description,created_by) VALUES($1,$2,$3,$4) RETURNING id,name,description,status,created_at,updated_at`, a.OrganizationID, name, strings.TrimSpace(desc), a.UserID).Scan(&v.ID, &v.Name, &v.Description, &v.Status, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}
func (s *Service) ListBases(ctx context.Context, a auth.Actor, page, limit int, search string) ([]KnowledgeBase, int, error) {
	off := (page - 1) * limit
	rows, err := s.db.Query(ctx, `SELECT id,name,description,status,created_at,updated_at,count(*) OVER() FROM knowledge_bases WHERE organization_id=$1 AND status='active' AND ($2='' OR name ILIKE '%'||$2||'%') ORDER BY updated_at DESC LIMIT $3 OFFSET $4`, a.OrganizationID, search, limit, off)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []KnowledgeBase
	total := 0
	for rows.Next() {
		var x KnowledgeBase
		if err = rows.Scan(&x.ID, &x.Name, &x.Description, &x.Status, &x.CreatedAt, &x.UpdatedAt, &total); err != nil {
			return nil, 0, err
		}
		out = append(out, x)
	}
	return out, total, rows.Err()
}
func (s *Service) GetBase(ctx context.Context, a auth.Actor, id uuid.UUID) (KnowledgeBase, error) {
	var x KnowledgeBase
	err := s.db.QueryRow(ctx, `SELECT id,name,description,status,created_at,updated_at FROM knowledge_bases WHERE organization_id=$1 AND id=$2 AND status='active'`, a.OrganizationID, id).Scan(&x.ID, &x.Name, &x.Description, &x.Status, &x.CreatedAt, &x.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = response.ErrNotFound
	}
	return x, err
}
func (s *Service) UpdateBase(ctx context.Context, a auth.Actor, id uuid.UUID, name, desc *string) (KnowledgeBase, error) {
	if !a.IsAdmin() {
		return KnowledgeBase{}, response.ErrForbidden
	}
	var x KnowledgeBase
	err := s.db.QueryRow(ctx, `UPDATE knowledge_bases SET name=COALESCE(NULLIF($3,''),name),description=COALESCE($4,description),updated_at=now() WHERE organization_id=$1 AND id=$2 AND status='active' RETURNING id,name,description,status,created_at,updated_at`, a.OrganizationID, id, name, desc).Scan(&x.ID, &x.Name, &x.Description, &x.Status, &x.CreatedAt, &x.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = response.ErrNotFound
	}
	return x, err
}
func (s *Service) DeleteBase(ctx context.Context, a auth.Actor, id uuid.UUID) error {
	if !a.IsAdmin() {
		return response.ErrForbidden
	}
	tag, err := s.db.Exec(ctx, `UPDATE knowledge_bases SET status='archived',updated_at=now() WHERE organization_id=$1 AND id=$2 AND status='active'`, a.OrganizationID, id)
	if err == nil && tag.RowsAffected() == 0 {
		return response.ErrNotFound
	}
	return err
}
func (s *Service) Upload(ctx context.Context, a auth.Actor, kb uuid.UUID, h *multipart.FileHeader) (Document, error) {
	if !a.IsAdmin() {
		return Document{}, response.ErrForbidden
	}
	if h.Size <= 0 || h.Size > s.maxBytes {
		return Document{}, response.E(422, "INVALID_FILE_SIZE", "File is empty or too large")
	}
	mime := detectMIME(h.Filename, h.Header.Get("Content-Type"))
	if !allowedMIME(mime) {
		return Document{}, response.E(422, "UNSUPPORTED_FILE_TYPE", "Supported file types: PDF, DOCX, TXT, MD")
	}
	if _, err := s.GetBase(ctx, a, kb); err != nil {
		return Document{}, err
	}
	f, err := h.Open()
	if err != nil {
		return Document{}, err
	}
	defer f.Close()
	buffered := bufio.NewReader(f)
	prefix, peekErr := buffered.Peek(512)
	if peekErr != nil && !errors.Is(peekErr, io.EOF) {
		return Document{}, peekErr
	}
	if !validFileSignature(mime, prefix) {
		return Document{}, response.E(422, "INVALID_FILE_CONTENT", "File content does not match its type")
	}
	key := uuid.NewString() + filepath.Ext(filepath.Base(h.Filename))
	obj, err := s.storage.Save(ctx, io.LimitReader(buffered, s.maxBytes+1), key)
	if err != nil {
		return Document{}, err
	}
	if obj.Size > s.maxBytes {
		_ = s.storage.Delete(ctx, key)
		return Document{}, response.E(422, "INVALID_FILE_SIZE", "File is too large")
	}
	d, err := s.insertDocument(ctx, a, kb, key, filepath.Base(h.Filename), mime, obj.Size, map[string]any{})
	if err != nil {
		_ = s.storage.Delete(ctx, key)
		return d, err
	}
	if s.processor != nil {
		s.processor.Enqueue(d.ID)
	}
	return d, nil
}
func (s *Service) AddText(ctx context.Context, a auth.Actor, kb uuid.UUID, title, content string) (Document, error) {
	if !a.IsAdmin() {
		return Document{}, response.ErrForbidden
	}
	if strings.TrimSpace(title) == "" || strings.TrimSpace(content) == "" {
		return Document{}, response.E(422, "VALIDATION_ERROR", "Title and content are required")
	}
	if int64(len(content)) > s.maxBytes {
		return Document{}, response.E(422, "INVALID_TEXT_SIZE", "Text is too large")
	}
	if _, err := s.GetBase(ctx, a, kb); err != nil {
		return Document{}, err
	}
	key := uuid.NewString() + ".txt"
	obj, err := s.storage.Save(ctx, strings.NewReader(content), key)
	if err != nil {
		return Document{}, err
	}
	d, err := s.insertDocument(ctx, a, kb, key, title+".txt", "text/plain", obj.Size, map[string]any{"title": title, "source": "text"})
	if err != nil {
		_ = s.storage.Delete(ctx, key)
		return d, err
	}
	if s.processor != nil {
		s.processor.Enqueue(d.ID)
	}
	return d, nil
}
func (s *Service) insertDocument(ctx context.Context, a auth.Actor, kb uuid.UUID, key, name, mime string, size int64, meta any) (Document, error) {
	b, _ := json.Marshal(meta)
	var d Document
	err := s.db.QueryRow(ctx, `INSERT INTO documents(organization_id,knowledge_base_id,filename,original_filename,mime_type,size_bytes,storage_path,metadata,created_by) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9) RETURNING id,knowledge_base_id,filename,original_filename,mime_type,size_bytes,status,error_message,metadata,created_at,updated_at`, a.OrganizationID, kb, key, name, mime, size, key, b, a.UserID).Scan(&d.ID, &d.KnowledgeBaseID, &d.Filename, &d.OriginalFilename, &d.MIMEType, &d.SizeBytes, &d.Status, &d.ErrorMessage, &d.Metadata, &d.CreatedAt, &d.UpdatedAt)
	return d, err
}
func (s *Service) GetDocument(ctx context.Context, a auth.Actor, id uuid.UUID) (Document, error) {
	var d Document
	err := s.db.QueryRow(ctx, `SELECT id,knowledge_base_id,filename,original_filename,mime_type,size_bytes,status,error_message,metadata,created_at,updated_at FROM documents WHERE organization_id=$1 AND id=$2`, a.OrganizationID, id).Scan(&d.ID, &d.KnowledgeBaseID, &d.Filename, &d.OriginalFilename, &d.MIMEType, &d.SizeBytes, &d.Status, &d.ErrorMessage, &d.Metadata, &d.CreatedAt, &d.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = response.ErrNotFound
	}
	return d, err
}
func (s *Service) ListDocuments(ctx context.Context, a auth.Actor, kb uuid.UUID, page, limit int, search string) ([]Document, int, error) {
	rows, err := s.db.Query(ctx, `SELECT id,knowledge_base_id,filename,original_filename,mime_type,size_bytes,status,error_message,metadata,created_at,updated_at,count(*) OVER() FROM documents WHERE organization_id=$1 AND knowledge_base_id=$2 AND ($3='' OR original_filename ILIKE '%'||$3||'%') ORDER BY created_at DESC LIMIT $4 OFFSET $5`, a.OrganizationID, kb, search, limit, (page-1)*limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Document
	total := 0
	for rows.Next() {
		var d Document
		if err = rows.Scan(&d.ID, &d.KnowledgeBaseID, &d.Filename, &d.OriginalFilename, &d.MIMEType, &d.SizeBytes, &d.Status, &d.ErrorMessage, &d.Metadata, &d.CreatedAt, &d.UpdatedAt, &total); err != nil {
			return nil, 0, err
		}
		out = append(out, d)
	}
	return out, total, rows.Err()
}
func (s *Service) DeleteDocument(ctx context.Context, a auth.Actor, id uuid.UUID) error {
	if !a.IsAdmin() {
		return response.ErrForbidden
	}
	var path string
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `DELETE FROM documents WHERE organization_id=$1 AND id=$2 RETURNING storage_path`, a.OrganizationID, id).Scan(&path)
	if errors.Is(err, pgx.ErrNoRows) {
		return response.ErrNotFound
	}
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	if err = s.storage.Delete(ctx, path); err != nil {
		return fmt.Errorf("document row removed but storage cleanup failed: %w", err)
	}
	return nil
}
func allowedMIME(m string) bool {
	return TextParser{}.Supports(m) || PDFParser{}.Supports(m) || DOCXParser{}.Supports(m)
}
func validFileSignature(mime string, b []byte) bool {
	switch mime {
	case "application/pdf":
		return bytes.HasPrefix(b, []byte("%PDF-"))
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return bytes.HasPrefix(b, []byte("PK\x03\x04"))
	case "text/plain", "text/markdown", "text/x-markdown":
		return utf8.Valid(b) && !bytes.Contains(b, []byte{0})
	default:
		return false
	}
}

type Processor struct {
	db       *pgxpool.Pool
	storage  storage.Storage
	provider providers.Provider
	parsers  []DocumentParser
	chunker  Chunker
	jobs     chan uuid.UUID
	workers  int
	log      *slog.Logger
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

func NewProcessor(db *pgxpool.Pool, st storage.Storage, p providers.Provider, c Chunker, workers int, log *slog.Logger) *Processor {
	ctx, cancel := context.WithCancel(context.Background())
	return &Processor{db: db, storage: st, provider: p, parsers: []DocumentParser{PDFParser{}, DOCXParser{}, TextParser{}}, chunker: c, jobs: make(chan uuid.UUID, workers*32), workers: workers, log: log, ctx: ctx, cancel: cancel}
}
func (p *Processor) Start(ctx context.Context) error {
	// Reclaim only stale leases. Resetting every processing row would allow two
	// application instances to index the same document concurrently.
	_, err := p.db.Exec(ctx, `UPDATE documents SET status='uploaded',error_message='Recovered after an interrupted processing lease',updated_at=now() WHERE status='processing' AND updated_at < now()-interval '15 minutes'`)
	if err != nil {
		return err
	}
	for i := 0; i < p.workers; i++ {
		p.wg.Add(1)
		go p.worker()
	}
	p.wg.Add(1)
	go p.poller()
	if err = p.enqueuePending(ctx); err != nil {
		p.Stop()
		return err
	}
	return nil
}
func (p *Processor) Enqueue(id uuid.UUID) {
	select {
	case p.jobs <- id:
	default:
		go func() {
			select {
			case p.jobs <- id:
			case <-p.ctx.Done():
			}
		}()
	}
}
func (p *Processor) Stop() { p.cancel(); p.wg.Wait() }
func (p *Processor) poller() {
	defer p.wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			if _, err := p.db.Exec(p.ctx, `UPDATE documents SET status='uploaded',error_message='Recovered after an interrupted processing lease',updated_at=now() WHERE status='processing' AND updated_at < now()-interval '15 minutes'`); err != nil && !errors.Is(err, context.Canceled) {
				p.log.Error("document lease recovery failed", "error", err)
			}
			if err := p.enqueuePending(p.ctx); err != nil && !errors.Is(err, context.Canceled) {
				p.log.Error("document queue polling failed", "error", err)
			}
		}
	}
}
func (p *Processor) enqueuePending(ctx context.Context) error {
	rows, err := p.db.Query(ctx, `SELECT id FROM documents WHERE status='uploaded' ORDER BY created_at LIMIT $1`, cap(p.jobs))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err = rows.Scan(&id); err != nil {
			return err
		}
		select {
		case p.jobs <- id:
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	return rows.Err()
}
func (p *Processor) worker() {
	defer p.wg.Done()
	for {
		select {
		case <-p.ctx.Done():
			return
		case id := <-p.jobs:
			if err := p.process(id); err != nil {
				p.log.Error("document processing failed", "document_id", id, "error", err)
				_, _ = p.db.Exec(context.Background(), `UPDATE documents SET status='failed',error_message=$2,updated_at=now() WHERE id=$1`, id, truncate(err.Error(), 2000))
			}
		}
	}
}
func (p *Processor) process(id uuid.UUID) error {
	ctx, cancel := context.WithTimeout(p.ctx, 10*time.Minute)
	defer cancel()
	var org, kb uuid.UUID
	var key, mime string
	err := p.db.QueryRow(ctx, `UPDATE documents SET status='processing',error_message=NULL,updated_at=now() WHERE id=$1 AND status='uploaded' RETURNING organization_id,knowledge_base_id,storage_path,mime_type`, id).Scan(&org, &kb, &key, &mime)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	f, err := p.storage.Open(ctx, key)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp("", "ai-doc-*")
	if err != nil {
		f.Close()
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	_, copyErr := io.Copy(tmp, f)
	f.Close()
	closeErr := tmp.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	var parser DocumentParser
	for _, x := range p.parsers {
		if x.Supports(mime) {
			parser = x
			break
		}
	}
	if parser == nil {
		return fmt.Errorf("no parser for %s", mime)
	}
	parsed, err := parser.Parse(ctx, tmpPath)
	if err != nil {
		return err
	}
	chunks := p.chunker.Split(parsed.Text)
	if len(chunks) == 0 {
		return errors.New("document contains no extractable text")
	}
	for start := 0; start < len(chunks); start += 64 {
		end := start + 64
		if end > len(chunks) {
			end = len(chunks)
		}
		emb, err := p.provider.Embed(ctx, chunks[start:end])
		if err != nil {
			return err
		}
		tx, err := p.db.Begin(ctx)
		if err != nil {
			return err
		}
		for i, text := range chunks[start:end] {
			meta, _ := json.Marshal(parsed.Metadata)
			_, err = tx.Exec(ctx, `INSERT INTO document_chunks(organization_id,document_id,knowledge_base_id,chunk_index,content,embedding,token_count,metadata) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(document_id,chunk_index) DO UPDATE SET content=EXCLUDED.content,embedding=EXCLUDED.embedding,token_count=EXCLUDED.token_count,metadata=EXCLUDED.metadata`, org, id, kb, start+i, text, pgvector.NewVector(emb[i]), len(strings.Fields(text)), meta)
			if err != nil {
				tx.Rollback(ctx)
				return err
			}
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
	}
	_, err = p.db.Exec(ctx, `UPDATE documents SET status='ready',updated_at=now() WHERE id=$1`, id)
	return err
}
func truncate(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
