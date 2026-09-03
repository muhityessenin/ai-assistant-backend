package knowledge

import (
	"strings"
	"testing"
)

func TestChunkerPreservesContentAndBoundaries(t *testing.T) {
	text := "First sentence. Second sentence.\n\n" + strings.Repeat("Long paragraph with useful words. ", 20)
	chunks := (Chunker{Size: 120, Overlap: 20}).Split(text)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if strings.TrimSpace(c) == "" {
			t.Fatalf("empty chunk %d", i)
		}
		if len(c) > 140 {
			t.Fatalf("unbounded chunk %d: %d", i, len(c))
		}
	}
}
func TestChunkerRejectsEmptyText(t *testing.T) {
	if got := (Chunker{Size: 100, Overlap: 10}).Split(" \n\n "); len(got) != 0 {
		t.Fatalf("got %v", got)
	}
}
func TestUploadMagicValidation(t *testing.T) {
	if !validFileSignature("application/pdf", []byte("%PDF-1.7")) {
		t.Fatal("valid PDF rejected")
	}
	if validFileSignature("application/pdf", []byte("not a PDF")) {
		t.Fatal("spoofed PDF accepted")
	}
	if !validFileSignature("application/vnd.openxmlformats-officedocument.wordprocessingml.document", []byte("PK\x03\x04rest")) {
		t.Fatal("valid DOCX rejected")
	}
	if validFileSignature("text/plain", []byte{'a', 0, 'b'}) {
		t.Fatal("binary data accepted as text")
	}
}
