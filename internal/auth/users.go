package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/example/ai-assistants-platform/internal/platform/response"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type User struct {
	ID        uuid.UUID `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Role      string    `json:"role"`
	CanTrain  bool      `json:"can_train"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func (s *Service) ListUsers(ctx context.Context, a Actor, page, limit int, search string) ([]User, int, error) {
	if !a.IsAdmin() {
		return nil, 0, response.ErrForbidden
	}
	rows, err := s.db.Query(ctx, `SELECT u.id,u.email,u.name,u.status,ou.role,ou.can_train,u.created_at,u.updated_at,count(*) OVER() FROM organization_users ou JOIN users u ON u.id=ou.user_id WHERE ou.organization_id=$1 AND ($2='' OR u.name ILIKE '%'||$2||'%' OR u.email ILIKE '%'||$2||'%') ORDER BY u.name LIMIT $3 OFFSET $4`, a.OrganizationID, search, limit, (page-1)*limit)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []User
	total := 0
	for rows.Next() {
		var x User
		if err = rows.Scan(&x.ID, &x.Email, &x.Name, &x.Status, &x.Role, &x.CanTrain, &x.CreatedAt, &x.UpdatedAt, &total); err != nil {
			return nil, 0, err
		}
		out = append(out, x)
	}
	return out, total, rows.Err()
}
func (s *Service) GetUser(ctx context.Context, a Actor, id uuid.UUID) (User, error) {
	if !a.IsAdmin() && a.UserID != id {
		return User{}, response.ErrForbidden
	}
	var x User
	err := s.db.QueryRow(ctx, `SELECT u.id,u.email,u.name,u.status,ou.role,ou.can_train,u.created_at,u.updated_at FROM organization_users ou JOIN users u ON u.id=ou.user_id WHERE ou.organization_id=$1 AND u.id=$2`, a.OrganizationID, id).Scan(&x.ID, &x.Email, &x.Name, &x.Status, &x.Role, &x.CanTrain, &x.CreatedAt, &x.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = response.ErrNotFound
	}
	return x, err
}
func (s *Service) CreateUser(ctx context.Context, a Actor, name, email, password, role string, canTrain bool) (User, error) {
	if !a.IsAdmin() {
		return User{}, response.ErrForbidden
	}
	if err := validateCredentials(email, password); err != nil {
		return User{}, err
	}
	if role != "admin" && role != "employee" {
		return User{}, response.E(422, "VALIDATION_ERROR", "role must be admin or employee")
	}
	if a.Role == "admin" && role == "admin" {
		return User{}, response.ErrForbidden
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback(ctx)
	x := User{ID: uuid.New(), Email: strings.ToLower(strings.TrimSpace(email)), Name: strings.TrimSpace(name), Role: role, Status: "active", CanTrain: canTrain || role == "admin"}
	err = tx.QueryRow(ctx, `INSERT INTO users(id,email,name,password_hash) VALUES($1,$2,$3,$4) RETURNING created_at,updated_at`, x.ID, x.Email, x.Name, hashPassword(password)).Scan(&x.CreatedAt, &x.UpdatedAt)
	if err != nil {
		return x, conflict(err)
	}
	_, err = tx.Exec(ctx, `INSERT INTO organization_users(organization_id,user_id,role,can_train) VALUES($1,$2,$3,$4)`, a.OrganizationID, x.ID, role, x.CanTrain)
	if err != nil {
		return x, err
	}
	return x, tx.Commit(ctx)
}
func (s *Service) UpdateUser(ctx context.Context, a Actor, id uuid.UUID, name, role, status *string, canTrain *bool) (User, error) {
	if !a.IsAdmin() && a.UserID != id {
		return User{}, response.ErrForbidden
	}
	current, err := s.GetUser(ctx, a, id)
	if err != nil {
		return User{}, err
	}
	if !a.IsAdmin() {
		role = nil
		status = nil
		canTrain = nil
	}
	if current.Role == "owner" && (role != nil || status != nil && *status != "active" || canTrain != nil && !*canTrain) {
		return User{}, response.E(409, "OWNER_PROTECTED", "Organization owner cannot be demoted or deactivated")
	}
	if a.Role == "admin" && (current.Role == "owner" || role != nil && *role == "admin") {
		return User{}, response.ErrForbidden
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return User{}, err
	}
	defer tx.Rollback(ctx)
	_, err = tx.Exec(ctx, `UPDATE users SET name=COALESCE($3,name),status=COALESCE($4,status),updated_at=now() WHERE id=$1 AND EXISTS(SELECT 1 FROM organization_users WHERE organization_id=$2 AND user_id=$1)`, id, a.OrganizationID, name, status)
	if err != nil {
		return User{}, err
	}
	if role != nil {
		if *role != "admin" && *role != "employee" {
			return User{}, response.E(422, "VALIDATION_ERROR", "Invalid role")
		}
		_, err = tx.Exec(ctx, `UPDATE organization_users SET role=$3 WHERE organization_id=$1 AND user_id=$2`, a.OrganizationID, id, *role)
		if err != nil {
			return User{}, err
		}
	}
	if canTrain != nil {
		_, err = tx.Exec(ctx, `UPDATE organization_users SET can_train=$3 WHERE organization_id=$1 AND user_id=$2`, a.OrganizationID, id, *canTrain)
		if err != nil {
			return User{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return User{}, err
	}
	return s.GetUser(ctx, a, id)
}
func (s *Service) DeactivateUser(ctx context.Context, a Actor, id uuid.UUID) error {
	inactive := "inactive"
	_, err := s.UpdateUser(ctx, a, id, nil, nil, &inactive, nil)
	return err
}
