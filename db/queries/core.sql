-- name: Ping :one
SELECT 1;

-- name: CountOrganizations :one
SELECT count(*) FROM organizations;

-- name: GetUserAuthByEmail :one
SELECT u.id, u.email, u.name, u.password_hash, u.status,
       ou.organization_id, ou.role
FROM users u JOIN organization_users ou ON ou.user_id = u.id
WHERE lower(u.email) = lower($1) AND u.status = 'active'
ORDER BY ou.created_at LIMIT 1;

-- name: GetAssistantForOrganization :one
SELECT * FROM assistants WHERE id=$1 AND organization_id=$2 AND status <> 'archived';

-- name: GetConversationForOrganization :one
SELECT * FROM conversations WHERE id=$1 AND organization_id=$2 AND status <> 'deleted';

-- name: ListRecentMessages :many
SELECT * FROM messages WHERE organization_id=$1 AND conversation_id=$2
ORDER BY created_at DESC LIMIT $3;
