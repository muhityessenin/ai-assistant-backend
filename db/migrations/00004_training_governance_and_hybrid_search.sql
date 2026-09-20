-- +goose Up
ALTER TABLE organization_users
  ADD COLUMN can_train boolean NOT NULL DEFAULT false;

UPDATE organization_users
SET can_train=true
WHERE role IN ('owner','admin');

ALTER TABLE assistant_training_entries
  ADD COLUMN status feedback_status NOT NULL DEFAULT 'approved',
  ADD COLUMN scope text NOT NULL DEFAULT 'organization'
    CHECK (scope IN ('private','organization')),
  ADD COLUMN version integer NOT NULL DEFAULT 1 CHECK (version > 0),
  ADD COLUMN reviewed_by uuid,
  ADD COLUMN reviewed_at timestamptz,
  ADD COLUMN updated_at timestamptz NOT NULL DEFAULT now(),
  ADD CONSTRAINT assistant_training_entries_reviewer_fk
    FOREIGN KEY(organization_id,reviewed_by)
    REFERENCES organization_users(organization_id,user_id);

CREATE INDEX assistant_training_entries_review_idx
  ON assistant_training_entries(organization_id,status,created_at DESC);
CREATE INDEX assistant_training_entries_approved_scope_idx
  ON assistant_training_entries(organization_id,assistant_id,scope,created_at DESC)
  WHERE status='approved';

CREATE INDEX document_chunks_content_fts_idx
  ON document_chunks
  USING gin (to_tsvector('simple',content));

-- +goose Down
DROP INDEX IF EXISTS document_chunks_content_fts_idx;
DROP INDEX IF EXISTS assistant_training_entries_approved_scope_idx;
DROP INDEX IF EXISTS assistant_training_entries_review_idx;

ALTER TABLE assistant_training_entries
  DROP CONSTRAINT IF EXISTS assistant_training_entries_reviewer_fk,
  DROP COLUMN IF EXISTS updated_at,
  DROP COLUMN IF EXISTS reviewed_at,
  DROP COLUMN IF EXISTS reviewed_by,
  DROP COLUMN IF EXISTS version,
  DROP COLUMN IF EXISTS scope,
  DROP COLUMN IF EXISTS status;

ALTER TABLE organization_users
  DROP COLUMN IF EXISTS can_train;
