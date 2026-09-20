-- +goose Up
CREATE TABLE assistant_publications (
 id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
 organization_id uuid NOT NULL,
 assistant_id uuid NOT NULL,
 enabled boolean NOT NULL DEFAULT true,
 created_by uuid NOT NULL REFERENCES users(id),
 created_at timestamptz NOT NULL DEFAULT now(),
 updated_at timestamptz NOT NULL DEFAULT now(),
 UNIQUE (organization_id, assistant_id),
 FOREIGN KEY (organization_id, assistant_id) REFERENCES assistants(organization_id, id) ON DELETE CASCADE
);
CREATE INDEX assistant_publications_public_idx ON assistant_publications(id) WHERE enabled;

-- +goose Down
DROP TABLE IF EXISTS assistant_publications;
