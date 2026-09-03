# Architecture audit

## Decision

The platform is a modular monolith. An assistant is a tenant-scoped database
configuration; no assistant type is represented in Go. HTTP handlers delegate to
services, services enforce tenant/RBAC invariants, domain repositories wrap typed
database access, and PostgreSQL/pgvector is the single durable store. File parsing
and embedding run in an in-process, bounded worker pool. LLM and storage are ports.

The MVP deliberately has no broker, cache, search engine, or separate worker. A
bounded in-memory queue is acceptable because document state is durable and
`uploaded`/interrupted `processing` documents are recovered at startup.

## Modules and flow

```text
cmd/api -> app composition/root router
  auth, assistant, conversation, knowledge, feedback (handler/service/repository)
  ai (orchestrator/context builder/provider registry/OpenAI adapter)
  storage (local adapter)
  platform (config, DB, middleware, response, workers)

request -> middleware -> handler -> service -> repository -> pgx/sqlc -> PostgreSQL
chat -> authorization -> parallel RAG/feedback retrieval -> context builder
     -> provider stream -> SSE -> durable assistant message
```

Tenant identity and user identity only come from verified access tokens. Every
tenant-owned query includes `organization_id`; access to conversations is owner-only
for employees and organization-wide for admins/owners. Assistant access is the
union of `is_available_for_all_users` and `assistant_access` membership.

## ER model

```text
organizations 1--* organization_users *--1 users
organizations 1--* assistants *--* users (assistant_access)
assistants *--* knowledge_bases (assistant_knowledge_bases)
knowledge_bases 1--* documents 1--* document_chunks(vector)
assistants 1--* conversations 1--* messages
messages 1--0..1 message_feedback 1--0..1 feedback_examples(vector)
users/assistants/conversations/knowledge all carry organization_id
refresh_tokens belongs to user + organization and stores only a SHA-256 token hash
```

All foreign keys that cross tenant-owned entities are backed by composite unique
keys and composite foreign keys where practical. Vector indexes use HNSW/cosine.

## API inventory

- System: `GET /health`, `/ready`, `/docs`, `/openapi.yaml`.
- Auth: `POST /api/v1/auth/{register,login,refresh,logout}`, `GET /api/v1/me`.
- Users: list/create/get/update/deactivate under `/api/v1/users`.
- Assistants: CRUD, KB attach/detach, user access grant/revoke/list.
- Knowledge: KB CRUD; document list/upload/get/delete; text ingestion.
- Conversations: list/get/rename/delete; create beneath an assistant; SSE messages.
- Feedback: submit beneath a message; admin list/get/moderate/delete.
- Administration: `GET /api/v1/admin/stats`.

Lists use `page`/`limit` (maximum 100), search/filter fields, and stable sort choices.
Responses are `{data,meta}` or `{error:{code,message,details}}`.

## Principal interfaces

```go
type Provider interface { StreamChat(context.Context, ChatRequest) (<-chan ChatChunk, error); Embed(context.Context, []string) ([][]float32, error) }
type Storage interface { Save(context.Context, io.Reader, string) (Object, error); Open(context.Context, string) (io.ReadCloser, error); Delete(context.Context, string) error }
type DocumentParser interface { Supports(string) bool; Parse(context.Context, string) (ParsedDocument, error) }
type KnowledgeRetriever interface { Retrieve(context.Context, uuid.UUID, uuid.UUID, string, RetrieveOptions) ([]KnowledgeChunk, error) }
type Orchestrator interface { Generate(context.Context, GenerateRequest) (<-chan StreamEvent, error) }
```

Provider calls are never inside DB transactions. Independent retrieval is parallel.
The OpenAI adapter is replaceable through the provider registry, while model choice
stays in each assistant row and therefore changes without deployment.
