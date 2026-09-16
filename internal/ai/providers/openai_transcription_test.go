package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOpenAITranscribeSendsMultipartAudio(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/audio/transcriptions" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Fatal("missing provider authorization")
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatal(err)
		}
		if r.FormValue("model") != "gpt-4o-mini-transcribe" {
			t.Fatalf("unexpected model %q", r.FormValue("model"))
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if header.Filename != "voice.webm" || header.Header.Get("Content-Type") != "audio/webm" {
			t.Fatalf("unexpected audio metadata: %s %s", header.Filename, header.Header.Get("Content-Type"))
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"text": "  hello from voice  "})
	}))
	defer server.Close()

	provider := NewOpenAI("test-key", server.URL, "embedding", 3, time.Second)
	text, err := provider.Transcribe(context.Background(), "gpt-4o-mini-transcribe", "voice.webm", "audio/webm", bytes.NewBufferString("audio-data"))
	if err != nil {
		t.Fatal(err)
	}
	if text != "hello from voice" {
		t.Fatalf("unexpected transcription %q", text)
	}
}
