-- +goose Up
ALTER TABLE conversations
  ADD COLUMN mode text NOT NULL DEFAULT 'work'
  CHECK (mode IN ('work', 'train'));

CREATE TABLE assistant_training_entries (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 organization_id uuid NOT NULL,
 assistant_id uuid NOT NULL,
 conversation_id uuid NOT NULL,
 source_message_id uuid NOT NULL,
 created_by uuid NOT NULL,
 content text NOT NULL CHECK(length(content) BETWEEN 1 AND 100000),
 embedding vector(1536) NOT NULL,
 created_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE(source_message_id),
 UNIQUE(organization_id,id),
 FOREIGN KEY(organization_id,assistant_id) REFERENCES assistants(organization_id,id) ON DELETE CASCADE,
 FOREIGN KEY(organization_id,conversation_id) REFERENCES conversations(organization_id,id) ON DELETE CASCADE,
 FOREIGN KEY(organization_id,source_message_id) REFERENCES messages(organization_id,id) ON DELETE CASCADE,
 FOREIGN KEY(organization_id,created_by) REFERENCES organization_users(organization_id,user_id)
);
CREATE INDEX assistant_training_entries_scope_idx ON assistant_training_entries(organization_id,assistant_id,created_at DESC);
CREATE INDEX assistant_training_entries_embedding_hnsw ON assistant_training_entries USING hnsw (embedding vector_cosine_ops);

-- +goose Down
DROP TABLE IF EXISTS assistant_training_entries;
ALTER TABLE conversations DROP COLUMN IF EXISTS mode;
