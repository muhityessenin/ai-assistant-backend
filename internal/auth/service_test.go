package auth

import (
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"testing"
	"time"
)

func TestPasswordHashRoundTrip(t *testing.T) {
	hash := hashPassword("a-strong-password")
	if !verifyPassword(hash, "a-strong-password") {
		t.Fatal("valid password rejected")
	}
	if verifyPassword(hash, "wrong-password") {
		t.Fatal("invalid password accepted")
	}
}
func TestAccessTokenRejectsWrongAudience(t *testing.T) {
	s := &Service{secret: []byte("01234567890123456789012345678901")}
	uid, oid := uuid.New(), uuid.New()
	makeToken := func(aud string) string {
		c := Claims{OrganizationID: oid.String(), Role: "employee", RegisteredClaims: jwt.RegisteredClaims{Issuer: "ai-platform", Audience: jwt.ClaimStrings{aud}, Subject: uid.String(), ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Minute))}}
		x, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString(s.secret)
		if err != nil {
			t.Fatal(err)
		}
		return x
	}
	if _, err := s.Parse(makeToken("wrong")); err == nil {
		t.Fatal("wrong audience accepted")
	}
	if _, err := s.Parse(makeToken("ai-platform-api")); err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
}
func TestRBACRoles(t *testing.T) {
	tests := []struct {
		role         string
		admin, owner bool
	}{{"owner", true, true}, {"admin", true, false}, {"employee", false, false}, {"", false, false}}
	for _, tt := range tests {
		a := Actor{UserID: uuid.New(), OrganizationID: uuid.New(), Role: tt.role}
		if a.IsAdmin() != tt.admin || a.IsOwner() != tt.owner {
			t.Fatalf("role %s policy mismatch", tt.role)
		}
	}
}
