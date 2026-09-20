package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type flushWriter struct {
	*httptest.ResponseRecorder
	flushed bool
}

func TestLimiterUsesForwardedIPOnlyFromTrustedProxy(t *testing.T) {
	limiter := NewLimiter(1, time.Minute)
	handler := limiter.Middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	request := func(forwarded string) int {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("X-Forwarded-For", forwarded)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, req)
		return response.Code
	}
	if got := request("203.0.113.10"); got != http.StatusNoContent {
		t.Fatalf("first forwarded visitor status = %d", got)
	}
	if got := request("203.0.113.10"); got != http.StatusTooManyRequests {
		t.Fatalf("repeated forwarded visitor status = %d", got)
	}
	if got := request("203.0.113.11"); got != http.StatusNoContent {
		t.Fatalf("second forwarded visitor status = %d", got)
	}
}

func (f *flushWriter) Flush() { f.flushed = true }
func TestLoggingRecorderPreservesFlusher(t *testing.T) {
	fw := &flushWriter{ResponseRecorder: httptest.NewRecorder()}
	rw := &recorder{ResponseWriter: fw, status: 200}
	var w http.ResponseWriter = rw
	fl, ok := w.(http.Flusher)
	if !ok {
		t.Fatal("SSE flusher was hidden")
	}
	fl.Flush()
	if !fw.flushed {
		t.Fatal("flush not delegated")
	}
}
