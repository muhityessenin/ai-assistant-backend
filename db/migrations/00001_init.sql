-- +goose Up
CREATE EXTENSION IF NOT EXISTS vector;
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TYPE organization_status AS ENUM ('active','suspended');
CREATE TYPE user_status AS ENUM ('active','inactive');
CREATE TYPE organization_role AS ENUM ('owner','admin','employee');
CREATE TYPE assistant_status AS ENUM ('active','archived');
CREATE TYPE document_status AS ENUM ('uploaded','processing','ready','failed');
CREATE TYPE conversation_status AS ENUM ('active','deleted');
CREATE TYPE message_role AS ENUM ('user','assistant');
CREATE TYPE feedback_status AS ENUM ('pending','approved','rejected');

CREATE TABLE organizations (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), name text NOT NULL CHECK (length(name) BETWEEN 2 AND 200),
 slug text NOT NULL UNIQUE CHECK (slug ~ '^[a-z0-9]+(?:-[a-z0-9]+)*$'), status organization_status NOT NULL DEFAULT 'active',
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE users (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), email text NOT NULL UNIQUE, name text NOT NULL CHECK (length(name) BETWEEN 1 AND 200),
 password_hash text NOT NULL, status user_status NOT NULL DEFAULT 'active', created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE TABLE organization_users (
 organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE, user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 role organization_role NOT NULL, created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(organization_id,user_id)
);
CREATE INDEX organization_users_user_idx ON organization_users(user_id, organization_id);

CREATE TABLE assistants (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200), slug text NOT NULL, description text NOT NULL DEFAULT '',
 system_prompt text NOT NULL CHECK(length(system_prompt) BETWEEN 1 AND 50000), provider text NOT NULL, model text NOT NULL,
 temperature real NOT NULL DEFAULT .2 CHECK(temperature BETWEEN 0 AND 2), max_output_tokens integer NOT NULL DEFAULT 2048 CHECK(max_output_tokens BETWEEN 1 AND 32768),
 settings jsonb NOT NULL DEFAULT '{"rag":{"enabled":true,"top_k":8,"min_score":0.6},"feedback":{"enabled":true,"top_k":3},"history":{"message_limit":16},"citations":true}',
 is_available_for_all_users boolean NOT NULL DEFAULT false, status assistant_status NOT NULL DEFAULT 'active',
 created_by uuid NOT NULL REFERENCES users(id), created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(organization_id,slug), UNIQUE(organization_id,id)
);
CREATE INDEX assistants_org_status_idx ON assistants(organization_id,status,updated_at DESC);
CREATE TABLE assistant_access (
 organization_id uuid NOT NULL, assistant_id uuid NOT NULL, user_id uuid NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(), PRIMARY KEY(assistant_id,user_id),
 FOREIGN KEY(organization_id,assistant_id) REFERENCES assistants(organization_id,id) ON DELETE CASCADE,
 FOREIGN KEY(organization_id,user_id) REFERENCES organization_users(organization_id,user_id) ON DELETE CASCADE
);
CREATE INDEX assistant_access_user_idx ON assistant_access(organization_id,user_id);

CREATE TABLE knowledge_bases (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE,
 name text NOT NULL CHECK(length(name) BETWEEN 1 AND 200), description text NOT NULL DEFAULT '', status text NOT NULL DEFAULT 'active' CHECK(status IN ('active','archived')),
 created_by uuid NOT NULL REFERENCES users(id), created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), UNIQUE(organization_id,id)
);
CREATE INDEX knowledge_bases_org_idx ON knowledge_bases(organization_id,status,updated_at DESC);
CREATE TABLE assistant_knowledge_bases (
 organization_id uuid NOT NULL, assistant_id uuid NOT NULL, knowledge_base_id uuid NOT NULL, created_at timestamptz NOT NULL DEFAULT now(),
 PRIMARY KEY(assistant_id,knowledge_base_id),
 FOREIGN KEY(organization_id,assistant_id) REFERENCES assistants(organization_id,id) ON DELETE CASCADE,
 FOREIGN KEY(organization_id,knowledge_base_id) REFERENCES knowledge_bases(organization_id,id) ON DELETE CASCADE
);
CREATE INDEX assistant_kb_kb_idx ON assistant_knowledge_bases(organization_id,knowledge_base_id);
CREATE TABLE documents (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), organization_id uuid NOT NULL, knowledge_base_id uuid NOT NULL,
 filename text NOT NULL, original_filename text NOT NULL, mime_type text NOT NULL, size_bytes bigint NOT NULL CHECK(size_bytes >= 0), storage_path text NOT NULL,
 status document_status NOT NULL DEFAULT 'uploaded', error_message text, metadata jsonb NOT NULL DEFAULT '{}', created_by uuid NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(), UNIQUE(organization_id,id),
 FOREIGN KEY(organization_id,knowledge_base_id) REFERENCES knowledge_bases(organization_id,id) ON DELETE CASCADE
);
CREATE INDEX documents_org_kb_idx ON documents(organization_id,knowledge_base_id,status,created_at DESC);
CREATE INDEX documents_search_idx ON documents(organization_id,lower(original_filename));
CREATE TABLE document_chunks (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), organization_id uuid NOT NULL, document_id uuid NOT NULL, knowledge_base_id uuid NOT NULL,
 chunk_index integer NOT NULL, content text NOT NULL, embedding vector(1536) NOT NULL, token_count integer NOT NULL DEFAULT 0,
 metadata jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(document_id,chunk_index),
 FOREIGN KEY(organization_id,document_id) REFERENCES documents(organization_id,id) ON DELETE CASCADE,
 FOREIGN KEY(organization_id,knowledge_base_id) REFERENCES knowledge_bases(organization_id,id) ON DELETE CASCADE
);
CREATE INDEX document_chunks_scope_idx ON document_chunks(organization_id,knowledge_base_id,document_id);
CREATE INDEX document_chunks_embedding_hnsw ON document_chunks USING hnsw (embedding vector_cosine_ops);

CREATE TABLE conversations (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), organization_id uuid NOT NULL, assistant_id uuid NOT NULL, user_id uuid NOT NULL,
 title text NOT NULL DEFAULT 'New conversation', status conversation_status NOT NULL DEFAULT 'active', created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(organization_id,id), FOREIGN KEY(organization_id,assistant_id) REFERENCES assistants(organization_id,id),
 FOREIGN KEY(organization_id,user_id) REFERENCES organization_users(organization_id,user_id)
);
CREATE INDEX conversations_user_idx ON conversations(organization_id,user_id,updated_at DESC);
CREATE INDEX conversations_assistant_idx ON conversations(organization_id,assistant_id,updated_at DESC);
CREATE INDEX conversations_search_idx ON conversations(organization_id,lower(title));
CREATE TABLE messages (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), organization_id uuid NOT NULL, conversation_id uuid NOT NULL, role message_role NOT NULL, content text NOT NULL,
 provider text, model text, input_tokens integer NOT NULL DEFAULT 0, output_tokens integer NOT NULL DEFAULT 0, total_tokens integer NOT NULL DEFAULT 0,
 latency_ms bigint NOT NULL DEFAULT 0, metadata jsonb NOT NULL DEFAULT '{}', created_at timestamptz NOT NULL DEFAULT now(), UNIQUE(organization_id,id),
 FOREIGN KEY(organization_id,conversation_id) REFERENCES conversations(organization_id,id) ON DELETE CASCADE
);
CREATE INDEX messages_conversation_idx ON messages(organization_id,conversation_id,created_at,id);

CREATE TABLE message_feedback (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), organization_id uuid NOT NULL, assistant_id uuid NOT NULL, conversation_id uuid NOT NULL,
 message_id uuid NOT NULL, user_id uuid NOT NULL, rating smallint NOT NULL CHECK(rating IN (-1,0,1)), comment text, corrected_answer text,
 status feedback_status NOT NULL DEFAULT 'pending', created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(message_id,user_id), UNIQUE(organization_id,id), FOREIGN KEY(organization_id,assistant_id) REFERENCES assistants(organization_id,id),
 FOREIGN KEY(organization_id,conversation_id) REFERENCES conversations(organization_id,id) ON DELETE CASCADE,
 FOREIGN KEY(organization_id,message_id) REFERENCES messages(organization_id,id) ON DELETE CASCADE,
 FOREIGN KEY(organization_id,user_id) REFERENCES organization_users(organization_id,user_id)
);
CREATE INDEX feedback_filter_idx ON message_feedback(organization_id,assistant_id,status,rating,created_at DESC);
CREATE TABLE feedback_examples (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), organization_id uuid NOT NULL, assistant_id uuid NOT NULL, source_message_id uuid NOT NULL,
 input_text text NOT NULL, original_answer text NOT NULL, corrected_answer text NOT NULL, embedding vector(1536) NOT NULL,
 status feedback_status NOT NULL DEFAULT 'pending', created_at timestamptz NOT NULL DEFAULT now(), updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(source_message_id), FOREIGN KEY(organization_id,assistant_id) REFERENCES assistants(organization_id,id) ON DELETE CASCADE,
 FOREIGN KEY(organization_id,source_message_id) REFERENCES messages(organization_id,id) ON DELETE CASCADE
);
CREATE INDEX feedback_examples_scope_idx ON feedback_examples(organization_id,assistant_id,status);
CREATE INDEX feedback_examples_embedding_hnsw ON feedback_examples USING hnsw (embedding vector_cosine_ops);

CREATE TABLE refresh_tokens (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(), token_hash bytea NOT NULL UNIQUE, user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
 organization_id uuid NOT NULL REFERENCES organizations(id) ON DELETE CASCADE, expires_at timestamptz NOT NULL, revoked_at timestamptz,
 created_at timestamptz NOT NULL DEFAULT now(), last_used_at timestamptz
);
CREATE INDEX refresh_tokens_user_idx ON refresh_tokens(user_id,organization_id,expires_at) WHERE revoked_at IS NULL;

-- +goose Down
DROP TABLE IF EXISTS refresh_tokens, feedback_examples, message_feedback, messages, conversations, document_chunks, documents, assistant_knowledge_bases, knowledge_bases, assistant_access, assistants, organization_users, users, organizations CASCADE;
DROP TYPE IF EXISTS feedback_status, message_role, conversation_status, document_status, assistant_status, organization_role, user_status, organization_status;
