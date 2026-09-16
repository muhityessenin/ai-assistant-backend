package providers

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"path/filepath"
	"strings"
	"time"
)

type OpenAI struct {
	key, base, embeddingModel string
	dimensions                int
	client                    *http.Client
}

func NewOpenAI(key, base, embeddingModel string, dimensions int, timeout time.Duration) *OpenAI {
	return &OpenAI{key: key, base: base, embeddingModel: embeddingModel, dimensions: dimensions, client: &http.Client{Timeout: timeout}}
}
func (o *OpenAI) StreamChat(ctx context.Context, in ChatRequest) (<-chan ChatChunk, error) {
	if o.key == "" {
		return nil, errors.New("OPENAI_API_KEY is not configured")
	}
	body := map[string]any{"model": in.Model, "messages": in.Messages, "temperature": in.Temperature, "max_tokens": in.MaxTokens, "stream": true, "stream_options": map[string]bool{"include_usage": true}}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", o.base+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	o.headers(req)
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		x, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return nil, fmt.Errorf("openai chat status %d: %s", resp.StatusCode, sanitize(x))
	}
	out := make(chan ChatChunk)
	go func() {
		defer close(out)
		defer resp.Body.Close()
		scan := bufio.NewScanner(resp.Body)
		scan.Buffer(make([]byte, 64*1024), 1024*1024)
		for scan.Scan() {
			line := scan.Text()
			if !strings.HasPrefix(line, "data: ") {
				continue
			}
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				out <- ChatChunk{Done: true}
				return
			}
			var ev struct {
				Choices []struct {
					Delta struct {
						Content string `json:"content"`
					} `json:"delta"`
				} `json:"choices"`
				Usage struct {
					Prompt     int `json:"prompt_tokens"`
					Completion int `json:"completion_tokens"`
					Total      int `json:"total_tokens"`
				} `json:"usage"`
			}
			if err := json.Unmarshal([]byte(data), &ev); err != nil {
				out <- ChatChunk{Err: err}
				return
			}
			c := ChatChunk{Usage: Usage{ev.Usage.Prompt, ev.Usage.Completion, ev.Usage.Total}}
			if len(ev.Choices) > 0 {
				c.Delta = ev.Choices[0].Delta.Content
			}
			if c.Delta != "" || c.Usage.TotalTokens > 0 {
				select {
				case out <- c:
				case <-ctx.Done():
					return
				}
			}
		}
		if err := scan.Err(); err != nil {
			out <- ChatChunk{Err: err}
		}
	}()
	return out, nil
}
func (o *OpenAI) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if o.key == "" {
		return nil, errors.New("OPENAI_API_KEY is not configured")
	}
	body := map[string]any{"model": o.embeddingModel, "input": texts, "dimensions": o.dimensions, "encoding_format": "float"}
	b, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(ctx, "POST", o.base+"/embeddings", bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	o.headers(req)
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		x, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return nil, fmt.Errorf("openai embeddings status %d: %s", resp.StatusCode, sanitize(x))
	}
	var v struct {
		Data []struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, err
	}
	out := make([][]float32, len(texts))
	for _, x := range v.Data {
		if x.Index >= 0 && x.Index < len(out) {
			out[x.Index] = x.Embedding
		}
	}
	for _, x := range out {
		if len(x) != o.dimensions {
			return nil, errors.New("embedding dimension mismatch")
		}
	}
	return out, nil
}
func (o *OpenAI) Transcribe(ctx context.Context, model, filename, contentType string, audio io.Reader) (string, error) {
	if o.key == "" {
		return "", errors.New("OPENAI_API_KEY is not configured")
	}
	if strings.TrimSpace(model) == "" {
		return "", errors.New("transcription model is not configured")
	}
	filename = filepath.Base(filename)
	if filename == "." || filename == "" {
		filename = "voice-message.webm"
	}
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	reader, writer := io.Pipe()
	form := multipart.NewWriter(writer)
	go func() {
		header := make(textproto.MIMEHeader)
		safeName := strings.ReplaceAll(filename, `"`, "'")
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, safeName))
		header.Set("Content-Type", contentType)
		part, err := form.CreatePart(header)
		if err == nil {
			_, err = io.Copy(part, audio)
		}
		if err == nil {
			err = form.WriteField("model", model)
		}
		if err == nil {
			err = form.WriteField("response_format", "json")
		}
		if closeErr := form.Close(); err == nil {
			err = closeErr
		}
		_ = writer.CloseWithError(err)
	}()
	req, err := http.NewRequestWithContext(ctx, "POST", o.base+"/audio/transcriptions", reader)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+o.key)
	req.Header.Set("Content-Type", form.FormDataContentType())
	resp, err := o.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return "", fmt.Errorf("openai transcription status %d: %s", resp.StatusCode, sanitize(body))
	}
	var result struct {
		Text string `json:"text"`
	}
	if err = json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	result.Text = strings.TrimSpace(result.Text)
	if result.Text == "" {
		return "", errors.New("transcription is empty")
	}
	return result.Text, nil
}
func (o *OpenAI) headers(r *http.Request) {
	r.Header.Set("Authorization", "Bearer "+o.key)
	r.Header.Set("Content-Type", "application/json")
}
func sanitize(b []byte) string {
	s := string(b)
	if len(s) > 1000 {
		s = s[:1000]
	}
	return strings.ReplaceAll(s, "\n", " ")
}
