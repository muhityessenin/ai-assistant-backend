package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/example/ai-assistants-platform/internal/platform/response"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/argon2"
)

type Service struct {
	db                    *pgxpool.Pool
	secret                []byte
	accessTTL, refreshTTL time.Duration
	publicRegistration    bool
}
type Tokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
}
type Claims struct {
	OrganizationID, Role string
	jwt.RegisteredClaims
}

func New(db *pgxpool.Pool, secret string, a, r time.Duration, public bool) *Service {
	return &Service{db: db, secret: []byte(secret), accessTTL: a, refreshTTL: r, publicRegistration: public}
}

func (s *Service) Authenticate(ctx context.Context, email, password string) (Actor, Tokens, error) {
	var a Actor
	var hash, status string
	err := s.db.QueryRow(ctx, `SELECT u.id,u.email,u.name,u.password_hash,u.status,ou.organization_id,ou.role,ou.can_train FROM users u JOIN organization_users ou ON ou.user_id=u.id JOIN organizations o ON o.id=ou.organization_id WHERE lower(u.email)=lower($1) AND o.status='active' ORDER BY ou.created_at LIMIT 1`, strings.TrimSpace(email)).Scan(&a.UserID, &a.Email, &a.Name, &hash, &status, &a.OrganizationID, &a.Role, &a.CanTrain)
	if err != nil || status != "active" || !verifyPassword(hash, password) {
		return a, Tokens{}, response.ErrUnauthorized
	}
	t, err := s.issue(ctx, a)
	return a, t, err
}
func (s *Service) Register(ctx context.Context, org, name, email, password string) (Actor, Tokens, error) {
	if !s.publicRegistration {
		return Actor{}, Tokens{}, response.ErrForbidden
	}
	if err := validateCredentials(email, password); err != nil {
		return Actor{}, Tokens{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Actor{}, Tokens{}, err
	}
	defer tx.Rollback(ctx)
	a := Actor{UserID: uuid.New(), OrganizationID: uuid.New(), Email: strings.ToLower(strings.TrimSpace(email)), Name: strings.TrimSpace(name), Role: "owner", CanTrain: true}
	slug := slugify(org) + "-" + a.OrganizationID.String()[:8]
	if _, err = tx.Exec(ctx, `INSERT INTO organizations(id,name,slug) VALUES($1,$2,$3)`, a.OrganizationID, strings.TrimSpace(org), slug); err != nil {
		return a, Tokens{}, conflict(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO users(id,email,name,password_hash) VALUES($1,$2,$3,$4)`, a.UserID, a.Email, a.Name, hashPassword(password)); err != nil {
		return a, Tokens{}, conflict(err)
	}
	if _, err = tx.Exec(ctx, `INSERT INTO organization_users(organization_id,user_id,role) VALUES($1,$2,'owner')`, a.OrganizationID, a.UserID); err != nil {
		return a, Tokens{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return a, Tokens{}, err
	}
	t, err := s.issue(ctx, a)
	return a, t, err
}
func (s *Service) Parse(token string) (Actor, error) {
	c := &Claims{}
	parsed, err := jwt.ParseWithClaims(token, c, func(t *jwt.Token) (any, error) {
		if t.Method.Alg() != jwt.SigningMethodHS256.Alg() {
			return nil, errors.New("invalid algorithm")
		}
		return s.secret, nil
	}, jwt.WithExpirationRequired(), jwt.WithIssuer("ai-platform"), jwt.WithAudience("ai-platform-api"), jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || !parsed.Valid {
		return Actor{}, response.ErrUnauthorized
	}
	uid, err := uuid.Parse(c.Subject)
	if err != nil {
		return Actor{}, response.ErrUnauthorized
	}
	oid, err := uuid.Parse(c.OrganizationID)
	if err != nil {
		return Actor{}, response.ErrUnauthorized
	}
	return Actor{UserID: uid, OrganizationID: oid, Role: c.Role}, nil
}
func (s *Service) Refresh(ctx context.Context, raw string) (Tokens, error) {
	h := sha256.Sum256([]byte(raw))
	var id, uid, oid uuid.UUID
	var role string
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Tokens{}, err
	}
	defer tx.Rollback(ctx)
	err = tx.QueryRow(ctx, `SELECT rt.id,rt.user_id,rt.organization_id,ou.role FROM refresh_tokens rt JOIN organization_users ou ON ou.organization_id=rt.organization_id AND ou.user_id=rt.user_id JOIN users u ON u.id=rt.user_id WHERE rt.token_hash=$1 AND rt.revoked_at IS NULL AND rt.expires_at>now() AND u.status='active' FOR UPDATE`, h[:]).Scan(&id, &uid, &oid, &role)
	if err != nil {
		return Tokens{}, response.ErrUnauthorized
	}
	if _, err = tx.Exec(ctx, `UPDATE refresh_tokens SET revoked_at=now(),last_used_at=now() WHERE id=$1`, id); err != nil {
		return Tokens{}, err
	}
	tokens, err := s.issueWith(ctx, Actor{UserID: uid, OrganizationID: oid, Role: role}, tx)
	if err != nil {
		return Tokens{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return Tokens{}, err
	}
	return tokens, nil
}
func (s *Service) Logout(ctx context.Context, raw string) error {
	h := sha256.Sum256([]byte(raw))
	_, err := s.db.Exec(ctx, `UPDATE refresh_tokens SET revoked_at=COALESCE(revoked_at,now()) WHERE token_hash=$1`, h[:])
	return err
}
func (s *Service) LoadActor(ctx context.Context, a Actor) (Actor, error) {
	err := s.db.QueryRow(ctx, `SELECT u.email,u.name,ou.role,ou.can_train FROM users u JOIN organization_users ou ON ou.user_id=u.id WHERE u.id=$1 AND ou.organization_id=$2 AND u.status='active'`, a.UserID, a.OrganizationID).Scan(&a.Email, &a.Name, &a.Role, &a.CanTrain)
	if err != nil {
		return Actor{}, response.ErrUnauthorized
	}
	return a, nil
}
func (s *Service) issue(ctx context.Context, a Actor) (Tokens, error) {
	return s.issueWith(ctx, a, s.db)
}

type tokenExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func (s *Service) issueWith(ctx context.Context, a Actor, db tokenExecutor) (Tokens, error) {
	now := time.Now()
	exp := now.Add(s.accessTTL)
	claims := Claims{OrganizationID: a.OrganizationID.String(), Role: a.Role, RegisteredClaims: jwt.RegisteredClaims{Issuer: "ai-platform", Subject: a.UserID.String(), Audience: jwt.ClaimStrings{"ai-platform-api"}, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(exp), ID: uuid.NewString()}}
	access, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(s.secret)
	if err != nil {
		return Tokens{}, err
	}
	b := make([]byte, 48)
	if _, err = rand.Read(b); err != nil {
		return Tokens{}, err
	}
	refresh := base64.RawURLEncoding.EncodeToString(b)
	h := sha256.Sum256([]byte(refresh))
	_, err = db.Exec(ctx, `INSERT INTO refresh_tokens(token_hash,user_id,organization_id,expires_at) VALUES($1,$2,$3,$4)`, h[:], a.UserID, a.OrganizationID, now.Add(s.refreshTTL))
	if err != nil {
		return Tokens{}, err
	}
	return Tokens{access, refresh, "Bearer", int64(s.accessTTL.Seconds())}, nil
}
func (s *Service) Bootstrap(ctx context.Context, email, password, org string) error {
	if email == "" {
		return nil
	}
	var n int
	if err := s.db.QueryRow(ctx, `SELECT count(*) FROM organizations`).Scan(&n); err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	old := s.publicRegistration
	s.publicRegistration = true
	_, _, err := s.Register(ctx, org, "Owner", email, password)
	s.publicRegistration = old
	return err
}
func validateCredentials(email, password string) error {
	if !regexp.MustCompile(`^[^@\s]+@[^@\s]+\.[^@\s]+$`).MatchString(email) {
		return response.E(422, "INVALID_EMAIL", "Invalid email")
	}
	if len(password) < 12 {
		return response.E(422, "WEAK_PASSWORD", "Password must be at least 12 characters")
	}
	return nil
}
func hashPassword(p string) string {
	salt := make([]byte, 16)
	_, _ = rand.Read(salt)
	h := argon2.IDKey([]byte(p), salt, 1, 64*1024, 4, 32)
	return fmt.Sprintf("$argon2id$v=19$m=65536,t=1,p=4$%s$%s", base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(h))
}
func verifyPassword(encoded, p string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" || parts[2] != "v=19" {
		return false
	}
	var m, t, pn int
	for _, field := range strings.Split(parts[3], ",") {
		kv := strings.SplitN(field, "=", 2)
		if len(kv) != 2 {
			return false
		}
		n, err := strconv.Atoi(kv[1])
		if err != nil {
			return false
		}
		switch kv[0] {
		case "m":
			m = n
		case "t":
			t = n
		case "p":
			pn = n
		}
	}
	if m <= 0 || t <= 0 || pn <= 0 || pn > 255 {
		return false
	}
	salt, e := base64.RawStdEncoding.DecodeString(parts[4])
	if e != nil {
		return false
	}
	want, e := base64.RawStdEncoding.DecodeString(parts[5])
	if e != nil {
		return false
	}
	got := argon2.IDKey([]byte(p), salt, uint32(t), uint32(m), uint8(pn), uint32(len(want)))
	return subtleEqual(got, want)
}
func subtleEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var x byte
	for i := range a {
		x |= a[i] ^ b[i]
	}
	return x == 0
}
func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = regexp.MustCompile(`[^a-z0-9]+`).ReplaceAllString(s, "-")
	return strings.Trim(s, "-")
}
func conflict(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return response.ErrNotFound
	}
	return &response.Error{Status: http.StatusConflict, Code: "CONFLICT", Message: "Resource already exists", Details: map[string]any{}}
}
