# AI Assistants Backend Platform

Production-oriented, tenant-isolated backend for configurable AI assistants. A new
assistant is a row created through `POST /api/v1/assistants`; no assistant-specific
Go code, endpoint, or deployment is required.

The deployment consists of exactly two containers: the Go API (including migration
runner and document workers) and PostgreSQL 17 with pgvector. See
[`docs/architecture.md`](docs/architecture.md) for the architecture audit, ER model,
API inventory, interfaces, and trade-offs.

## Quick server deployment

Requirements: Linux, Docker Engine, and Docker Compose v2.

```bash
git clone <repository-url> ai-assistants-platform
cd ai-assistants-platform
cp .env.example .env
nano .env
docker compose up -d --build
docker compose ps
docker compose logs -f backend
```

At minimum, replace `POSTGRES_PASSWORD` in both the password and `DATABASE_URL`,
set a random `JWT_SECRET` of at least 32 characters, configure the bootstrap owner,
set `OPENAI_API_KEY`, and set `CORS_ALLOWED_ORIGINS`.

```bash
curl -fsS http://localhost:18473/health
curl -fsS http://localhost:18473/ready
```

The API is available at `http://SERVER:18473/api/v1`; documentation is at `/docs`,
with the machine-readable contract at `/openapi.yaml`. PostgreSQL uses the
non-standard host port 15439 only for administration; the backend always connects
to `postgres:5432` on the private network.

## Architecture and project layout

```text
cmd/api                 process lifecycle and graceful shutdown
internal/auth           JWT/refresh tokens, Argon2id, users and roles
internal/assistant      dynamic assistant configuration and access
internal/conversation   tenant/owner-scoped conversations and messages
internal/knowledge      parsers, chunker, workers and RAG retrieval
internal/feedback       ratings, corrections, moderation and retrieval
internal/training       persistent chat lessons and semantic memory retrieval
internal/ai             context builder and streaming orchestrator
internal/ai/providers   provider interface and OpenAI adapter
internal/storage        storage interface and local implementation
internal/platform       config, middleware, responses and PostgreSQL
internal/dbgen          checked-in sqlc-generated primitives
db/migrations           versioned Goose migrations
db/queries              sqlc query sources
docs                    architecture audit and OpenAPI
```

The dependency direction is handler → service → repository/typed query → database.
Identity, tenant, and role come only from a validated JWT. Employee conversation
queries require ownership, while admin/owner queries remain organization-scoped.
RAG requires both authenticated `organization_id` and a knowledge base attached to
the selected assistant. Composite database keys reinforce tenant relationships.

## Configuration

`.env.example` documents all settings. Important groups are:

- `APP_*`, `HTTP_*`, CORS, and logging for the HTTP runtime.
- `POSTGRES_*` and `DATABASE_URL`; keep `postgres:5432` inside Compose.
- JWT TTLs, `PUBLIC_REGISTRATION`, and idempotent `BOOTSTRAP_*` owner creation.
- `OPENAI_*`, default provider/model, and LLM timeout.
- `OPENAI_TRANSCRIPTION_MODEL` and `MAX_AUDIO_MB` for authenticated voice input.
- Upload, worker, chunk, RAG, feedback moderation, and rate-limit settings.

Embeddings are centrally set to 1536 dimensions. The OpenAI adapter requests that
dimension, matching the migration and HNSW indexes. Changing it requires a migration
and re-embedding existing data.

## Startup, migrations, and bootstrap

The API retries PostgreSQL, applies Goose migrations, performs bootstrap, recovers
interrupted documents, starts workers, and only then starts HTTP. Migration failure
terminates startup. `/ready` verifies PostgreSQL and the migration version.

Bootstrap runs only when no organization exists. All three bootstrap values are
required and the password must contain at least 12 characters. Restarts never
duplicate the owner, and passwords are never logged. Keep public registration off
in production unless self-service organization creation is intended.

## Typical API flow

1. Login at `POST /api/v1/auth/login` and use the Bearer access token.
2. Create a knowledge base and upload PDF, DOCX, TXT, or Markdown. Upload returns
   202; poll the document until `status=ready` or inspect `error_message`.
3. Create an assistant configuration, attach knowledge, and grant users access (or
   enable availability for all users).
4. Create a conversation and post a message. A conversation has a persistent
   `mode`: `work` for normal answers or `train` for teaching through chat. In train
   mode each user message is embedded as tenant- and assistant-isolated memory;
   relevant memories are retrieved in future work and train chats. Change the mode
   with `PATCH /api/v1/conversations/{id}` and `{\"mode\":\"train\"}`. This is
   application-level RAG memory, not provider model-weight fine-tuning. SSE emits
   `message_start`, `sources`, `content_delta`, and `message_complete`.
   Browser voice input is recorded as WebM, MP4, or OGG and posted to
   `POST /api/v1/audio/transcriptions`; the authenticated endpoint returns text
   which the user can review before sending as a normal work or train message.
5. Submit feedback. Approved corrections are embedded and retrieved for similar
   future questions.

Client disconnect cancels the provider context. A final assistant message is saved
only after a complete stream; the user message is durable before the network call.
No database transaction spans an LLM request.

## Security and operations

- Argon2id passwords; signed short-lived JWTs; opaque rotated refresh tokens stored
  only as SHA-256 hashes; logout revocation.
- Central roles, tenant-filtered parameterized SQL, assistant access, safe generated
  filenames, MIME/size/body limits, CORS allow-list, secure headers, recovery, and
  separate general/chat rate limits.
- JSON logs include request, user and organization IDs, status and duration. LLM
  logs include model, latency and token use. Secrets are not logged.
- Rate limits and the job queue are deliberately in-process MVP components. Durable
  document status makes restarts safe. Replace them before horizontal scaling.

Volumes `ai-platform-postgres-data` and `ai-platform-uploads` survive
`docker compose down`. Do not use `docker compose down -v` in production.

## Build and test

```bash
make build
make test
make sqlc
make migration name=add_feature
make docker-up
make logs
make docker-down
```

Tests cover password/RBAC, assistant-context boundaries, SSE flushing,
tenant-scoped RAG query invariants, and paragraph-aware chunking. CI should also run
repository tests against an isolated PostgreSQL/pgvector database.

## Database backup and restore

```bash
docker exec ai-platform-postgres pg_dump \
  -U ai_backend -d ai_assistants -Fc -f /tmp/ai_assistants.dump
docker cp ai-platform-postgres:/tmp/ai_assistants.dump ./ai_assistants.dump
```

Restore into an empty database during a maintenance window:

```bash
docker cp ./ai_assistants.dump ai-platform-postgres:/tmp/ai_assistants.dump
docker exec ai-platform-postgres pg_restore \
  -U ai_backend -d ai_assistants --clean --if-exists /tmp/ai_assistants.dump
```

Back up the uploads volume too; document rows reference those objects.

## Updating a deployed version

```bash
git pull --ff-only
docker compose build --pull backend
docker compose up -d
docker compose logs -f backend
curl -fsS http://localhost:18473/ready
```

Migrations are forward-applied by the backend. Take a backup before upgrades.

## Adding a provider or storage backend

Implement `providers.Provider` (`StreamChat`, `Embed`), register it in the composition
root, and store its name/model in assistant rows. ChatService and the orchestrator do
not change. Credentials remain environment secrets; assistant model/settings remain
live database configuration.

For S3/MinIO, implement `storage.Storage` (`Save`, `Open`, `Delete`) and replace the
adapter in `app.New`; knowledge business logic remains unchanged.

## Production notes

Terminate TLS at a load balancer/reverse proxy, firewall or remove the PostgreSQL
host port, rotate bootstrap credentials, monitor `/ready` and container health, ship
JSON logs, schedule database and upload backups, and pin reviewed image digests in
tightly controlled environments.
