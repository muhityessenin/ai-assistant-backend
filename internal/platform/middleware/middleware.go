package middleware

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/example/ai-assistants-platform/internal/auth"
	"github.com/example/ai-assistants-platform/internal/platform/response"
	"github.com/google/uuid"
)

type key string

const (
	actorKey     key = "actor"
	requestIDKey key = "request_id"
)

func Actor(ctx context.Context) (auth.Actor, bool) {
	a, ok := ctx.Value(actorKey).(auth.Actor)
	return a, ok
}
func GetRequestID(ctx context.Context) string { v, _ := ctx.Value(requestIDKey).(string); return v }
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" || len(id) > 100 {
			id = uuid.NewString()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), requestIDKey, id)))
	})
}
func Recover(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				log.Error("panic", "request_id", GetRequestID(r.Context()), "error", v, "stack", string(debug.Stack()))
				response.WriteError(w, response.E(500, "INTERNAL_ERROR", "Internal server error"))
			}
		}()
		next.ServeHTTP(w, r)
	})
}

type recorder struct {
	http.ResponseWriter
	status int
}

func (r *recorder) WriteHeader(s int) { r.status = s; r.ResponseWriter.WriteHeader(s) }
func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
func Logging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &recorder{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)
		a, _ := Actor(r.Context())
		log.Info("request", "request_id", GetRequestID(r.Context()), "user_id", a.UserID, "organization_id", a.OrganizationID, "method", r.Method, "path", r.URL.Path, "status", rw.status, "duration_ms", time.Since(start).Milliseconds())
	})
}
func SecureHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}
func CORS(origins []string, next http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, o := range origins {
		allowed[o] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		o := r.Header.Get("Origin")
		if o != "" && (allowed[o] || allowed["*"]) {
			w.Header().Set("Access-Control-Allow-Origin", o)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PATCH,DELETE,OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func Authenticate(s *auth.Service, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			response.WriteError(w, response.ErrUnauthorized)
			return
		}
		a, err := s.Parse(parts[1])
		if err != nil {
			response.WriteError(w, err)
			return
		}
		a, err = s.LoadActor(r.Context(), a)
		if err != nil {
			response.WriteError(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), actorKey, a)))
	})
}
func RequireAdmin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, ok := Actor(r.Context())
		if !ok || !a.IsAdmin() {
			response.WriteError(w, response.ErrForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
func RequireOwner(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a, ok := Actor(r.Context())
		if !ok || !a.IsOwner() {
			response.WriteError(w, response.ErrForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

type visitor struct {
	count int
	reset time.Time
}
type Limiter struct {
	mu     sync.Mutex
	v      map[string]visitor
	max    int
	window time.Duration
}

func NewLimiter(max int, window time.Duration) *Limiter {
	return &Limiter{v: map[string]visitor{}, max: max, window: window}
}
func (l *Limiter) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, _ := net.SplitHostPort(r.RemoteAddr)
		if a, ok := Actor(r.Context()); ok {
			host = a.UserID.String()
		}
		now := time.Now()
		l.mu.Lock()
		v := l.v[host]
		if now.After(v.reset) {
			v = visitor{reset: now.Add(l.window)}
		}
		v.count++
		l.v[host] = v
		allowed := v.count <= l.max
		if len(l.v) > 10000 {
			for k, x := range l.v {
				if now.After(x.reset) {
					delete(l.v, k)
				}
			}
		}
		l.mu.Unlock()
		if !allowed {
			response.WriteError(w, response.E(429, "RATE_LIMITED", "Too many requests"))
			return
		}
		next.ServeHTTP(w, r)
	})
}
