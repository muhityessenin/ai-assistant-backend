-- +goose NO TRANSACTION
-- +goose Up
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX CONCURRENTLY IF NOT EXISTS users_email_lower_idx
  ON users (lower(email));
CREATE INDEX CONCURRENTLY IF NOT EXISTS users_name_trgm_idx
  ON users USING gin (name gin_trgm_ops);
CREATE INDEX CONCURRENTLY IF NOT EXISTS users_email_trgm_idx
  ON users USING gin (email gin_trgm_ops);

CREATE INDEX CONCURRENTLY IF NOT EXISTS assistants_name_trgm_idx
  ON assistants USING gin (name gin_trgm_ops);
CREATE INDEX CONCURRENTLY IF NOT EXISTS assistants_description_trgm_idx
  ON assistants USING gin (description gin_trgm_ops);
CREATE INDEX CONCURRENTLY IF NOT EXISTS knowledge_bases_name_trgm_idx
  ON knowledge_bases USING gin (name gin_trgm_ops);
CREATE INDEX CONCURRENTLY IF NOT EXISTS documents_original_filename_trgm_idx
  ON documents USING gin (original_filename gin_trgm_ops);
CREATE INDEX CONCURRENTLY IF NOT EXISTS conversations_title_trgm_idx
  ON conversations USING gin (title gin_trgm_ops);

CREATE INDEX CONCURRENTLY IF NOT EXISTS conversations_org_active_updated_idx
  ON conversations (organization_id,updated_at DESC)
  WHERE status='active';
CREATE INDEX CONCURRENTLY IF NOT EXISTS documents_org_kb_created_idx
  ON documents (organization_id,knowledge_base_id,created_at DESC);
CREATE INDEX CONCURRENTLY IF NOT EXISTS documents_uploaded_created_idx
  ON documents (created_at)
  WHERE status='uploaded';
CREATE INDEX CONCURRENTLY IF NOT EXISTS messages_user_history_idx
  ON messages (organization_id,conversation_id,created_at DESC)
  WHERE role='user';

CREATE INDEX CONCURRENTLY IF NOT EXISTS feedback_org_created_idx
  ON message_feedback (organization_id,created_at DESC);
CREATE INDEX CONCURRENTLY IF NOT EXISTS feedback_org_status_created_idx
  ON message_feedback (organization_id,status,created_at DESC);
CREATE INDEX CONCURRENTLY IF NOT EXISTS feedback_org_user_created_idx
  ON message_feedback (organization_id,user_id,created_at DESC);

-- +goose Down
DROP INDEX CONCURRENTLY IF EXISTS feedback_org_user_created_idx;
DROP INDEX CONCURRENTLY IF EXISTS feedback_org_status_created_idx;
DROP INDEX CONCURRENTLY IF EXISTS feedback_org_created_idx;
DROP INDEX CONCURRENTLY IF EXISTS messages_user_history_idx;
DROP INDEX CONCURRENTLY IF EXISTS documents_uploaded_created_idx;
DROP INDEX CONCURRENTLY IF EXISTS documents_org_kb_created_idx;
DROP INDEX CONCURRENTLY IF EXISTS conversations_org_active_updated_idx;
DROP INDEX CONCURRENTLY IF EXISTS conversations_title_trgm_idx;
DROP INDEX CONCURRENTLY IF EXISTS documents_original_filename_trgm_idx;
DROP INDEX CONCURRENTLY IF EXISTS knowledge_bases_name_trgm_idx;
DROP INDEX CONCURRENTLY IF EXISTS assistants_description_trgm_idx;
DROP INDEX CONCURRENTLY IF EXISTS assistants_name_trgm_idx;
DROP INDEX CONCURRENTLY IF EXISTS users_email_trgm_idx;
DROP INDEX CONCURRENTLY IF EXISTS users_name_trgm_idx;
DROP INDEX CONCURRENTLY IF EXISTS users_email_lower_idx;
