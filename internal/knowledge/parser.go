package knowledge

import (
	"archive/zip"
	"context"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/ledongthuc/pdf"
)

type ParsedDocument struct {
	Text     string
	Metadata map[string]any
}
type DocumentParser interface {
	Supports(string) bool
	Parse(context.Context, string) (ParsedDocument, error)
}
type TextParser struct{}

func (TextParser) Supports(m string) bool {
	return m == "text/plain" || m == "text/markdown" || m == "text/x-markdown"
}
func (TextParser) Parse(ctx context.Context, p string) (ParsedDocument, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return ParsedDocument{}, err
	}
	if err = ctx.Err(); err != nil {
		return ParsedDocument{}, err
	}
	return ParsedDocument{Text: string(b), Metadata: map[string]any{}}, nil
}

type PDFParser struct{}

func (PDFParser) Supports(m string) bool { return m == "application/pdf" }
func (PDFParser) Parse(ctx context.Context, p string) (ParsedDocument, error) {
	f, r, err := pdf.Open(p)
	if err != nil {
		return ParsedDocument{}, err
	}
	defer f.Close()
	rd, err := r.GetPlainText()
	if err != nil {
		return ParsedDocument{}, err
	}
	b, err := io.ReadAll(rd)
	if err != nil {
		return ParsedDocument{}, err
	}
	if err = ctx.Err(); err != nil {
		return ParsedDocument{}, err
	}
	return ParsedDocument{Text: string(b), Metadata: map[string]any{"pages": r.NumPage()}}, nil
}

type DOCXParser struct{}

func (DOCXParser) Supports(m string) bool {
	return m == "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
}
func (DOCXParser) Parse(ctx context.Context, p string) (ParsedDocument, error) {
	z, err := zip.OpenReader(p)
	if err != nil {
		return ParsedDocument{}, err
	}
	defer z.Close()
	var file *zip.File
	for _, f := range z.File {
		if f.Name == "word/document.xml" {
			file = f
			break
		}
	}
	if file == nil {
		return ParsedDocument{}, errors.New("DOCX document.xml is missing")
	}
	r, err := file.Open()
	if err != nil {
		return ParsedDocument{}, err
	}
	defer r.Close()
	dec := xml.NewDecoder(r)
	var b strings.Builder
	for {
		if err := ctx.Err(); err != nil {
			return ParsedDocument{}, err
		}
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return ParsedDocument{}, err
		}
		switch x := tok.(type) {
		case xml.CharData:
			b.Write([]byte(x))
		case xml.EndElement:
			if x.Name.Local == "p" || x.Name.Local == "tr" {
				b.WriteByte('\n')
			}
		}
	}
	return ParsedDocument{Text: b.String(), Metadata: map[string]any{}}, nil
}
func detectMIME(name, declared string) string {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case ".md":
		return "text/markdown"
	case ".txt":
		return "text/plain"
	}
	return declared
}
func cleanText(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var b strings.Builder
	space := false
	nl := 0
	for _, r := range s {
		if r == '\n' {
			nl++
			space = false
			if nl <= 2 {
				b.WriteRune(r)
			}
			continue
		}
		nl = 0
		if unicode.IsSpace(r) {
			if !space {
				b.WriteByte(' ')
				space = true
			}
			continue
		}
		space = false
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}
