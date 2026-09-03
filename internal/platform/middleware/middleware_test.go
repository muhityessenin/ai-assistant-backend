package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

type flushWriter struct {
	*httptest.ResponseRecorder
	flushed bool
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
