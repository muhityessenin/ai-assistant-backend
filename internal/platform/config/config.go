package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

const EmbeddingDimensions = 1536

type Config struct {
	Env, Host, Port, DatabaseURL, JWTSecret                                               string
	AccessTTL, RefreshTTL, ReadTimeout, WriteTimeout, IdleTimeout, LLMTimeout             time.Duration
	OpenAIKey, OpenAIBaseURL, DefaultProvider, DefaultChatModel, EmbeddingModel           string
	UploadDir                                                                             string
	MaxUploadBytes                                                                        int64
	DocumentWorkers, ChunkSize, ChunkOverlap, DefaultTopK, RateRequests, ChatRateRequests int
	RateWindow                                                                            time.Duration
	CORS                                                                                  []string
	LogLevel                                                                              string
	PublicRegistration, AutoApproveAdminFeedback                                          bool
	BootstrapEmail, BootstrapPassword, BootstrapOrg                                       string
}

func Load() (cfg Config, err error) {
	defer func() {
		if v := recover(); v != nil {
			cfg = Config{}
			err = fmt.Errorf("invalid environment configuration: %v", v)
		}
	}()
	_ = godotenv.Load()
	c := Config{
		Env: env("APP_ENV", "production"), Host: env("APP_HOST", "0.0.0.0"), Port: env("APP_PORT", "18473"),
		DatabaseURL: os.Getenv("DATABASE_URL"), JWTSecret: os.Getenv("JWT_SECRET"), AccessTTL: duration("JWT_ACCESS_TTL", 15*time.Minute), RefreshTTL: duration("JWT_REFRESH_TTL", 30*24*time.Hour),
		ReadTimeout: duration("HTTP_READ_TIMEOUT", 15*time.Second), WriteTimeout: duration("HTTP_WRITE_TIMEOUT", 0), IdleTimeout: duration("HTTP_IDLE_TIMEOUT", 60*time.Second), LLMTimeout: duration("LLM_TIMEOUT", 2*time.Minute),
		OpenAIKey: os.Getenv("OPENAI_API_KEY"), OpenAIBaseURL: strings.TrimRight(env("OPENAI_BASE_URL", "https://api.openai.com/v1"), "/"),
		DefaultProvider: env("DEFAULT_AI_PROVIDER", "openai"), DefaultChatModel: env("OPENAI_CHAT_MODEL", "gpt-4o-mini"), EmbeddingModel: env("OPENAI_EMBEDDING_MODEL", "text-embedding-3-small"),
		UploadDir: env("UPLOAD_DIR", "./data/uploads"), MaxUploadBytes: int64(integer("MAX_UPLOAD_MB", 50)) << 20,
		DocumentWorkers: integer("DOCUMENT_WORKERS", 2), ChunkSize: integer("RAG_CHUNK_SIZE", 800), ChunkOverlap: integer("RAG_CHUNK_OVERLAP", 150), DefaultTopK: integer("RAG_DEFAULT_TOP_K", 8),
		RateRequests: integer("RATE_LIMIT_REQUESTS", 100), ChatRateRequests: integer("CHAT_RATE_LIMIT_REQUESTS", 20), RateWindow: duration("RATE_LIMIT_WINDOW", time.Minute),
		CORS: csv("CORS_ALLOWED_ORIGINS"), LogLevel: env("LOG_LEVEL", "info"), PublicRegistration: boolean("PUBLIC_REGISTRATION", false), AutoApproveAdminFeedback: boolean("AUTO_APPROVE_ADMIN_FEEDBACK", true),
		BootstrapEmail: strings.TrimSpace(os.Getenv("BOOTSTRAP_OWNER_EMAIL")), BootstrapPassword: os.Getenv("BOOTSTRAP_OWNER_PASSWORD"), BootstrapOrg: strings.TrimSpace(os.Getenv("BOOTSTRAP_ORGANIZATION_NAME")),
	}
	if c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL is required")
	}
	if len(c.JWTSecret) < 32 {
		return c, errors.New("JWT_SECRET must contain at least 32 characters")
	}
	if c.ChunkOverlap < 0 || c.ChunkOverlap >= c.ChunkSize {
		return c, errors.New("RAG_CHUNK_OVERLAP must be non-negative and smaller than RAG_CHUNK_SIZE")
	}
	if c.DocumentWorkers < 1 {
		return c, errors.New("DOCUMENT_WORKERS must be positive")
	}
	if (c.BootstrapEmail != "" || c.BootstrapPassword != "" || c.BootstrapOrg != "") && (c.BootstrapEmail == "" || len(c.BootstrapPassword) < 12 || c.BootstrapOrg == "") {
		return c, errors.New("all bootstrap values are required and bootstrap password must be at least 12 characters")
	}
	return c, nil
}
func env(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
}
func duration(k string, d time.Duration) time.Duration {
	v := env(k, "")
	if v == "" {
		return d
	}
	x, e := time.ParseDuration(v)
	if e != nil {
		panic(fmt.Sprintf("invalid %s: %v", k, e))
	}
	return x
}
func integer(k string, d int) int {
	v := env(k, "")
	if v == "" {
		return d
	}
	x, e := strconv.Atoi(v)
	if e != nil {
		panic(fmt.Sprintf("invalid %s", k))
	}
	return x
}
func boolean(k string, d bool) bool {
	v := env(k, "")
	if v == "" {
		return d
	}
	x, e := strconv.ParseBool(v)
	if e != nil {
		panic(fmt.Sprintf("invalid %s", k))
	}
	return x
}
func csv(k string) []string {
	var out []string
	for _, v := range strings.Split(os.Getenv(k), ",") {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
