package knowledge

import "strings"

type Chunker struct{ Size, Overlap int }

func (c Chunker) Split(text string) []string {
	text = cleanText(text)
	if text == "" {
		return nil
	}
	paragraphs := strings.Split(text, "\n\n")
	var chunks []string
	var cur strings.Builder
	flush := func() {
		s := strings.TrimSpace(cur.String())
		if s != "" {
			chunks = append(chunks, s)
		}
		cur.Reset()
	}
	for _, p := range paragraphs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if cur.Len()+len(p)+2 <= c.Size {
			if cur.Len() > 0 {
				cur.WriteString("\n\n")
			}
			cur.WriteString(p)
			continue
		}
		flush()
		for len(p) > c.Size {
			cut := bestCut(p, c.Size)
			chunks = append(chunks, strings.TrimSpace(p[:cut]))
			start := cut - c.Overlap
			if start < 0 {
				start = 0
			}
			p = strings.TrimSpace(p[start:])
		}
		cur.WriteString(p)
	}
	flush()
	return chunks
}
func bestCut(s string, n int) int {
	if len(s) <= n {
		return len(s)
	}
	floor := n / 2
	for i := n; i > floor; i-- {
		if s[i-1] == '.' || s[i-1] == '!' || s[i-1] == '?' || s[i-1] == '\n' {
			return i
		}
	}
	for i := n; i > floor; i-- {
		if s[i-1] == ' ' {
			return i
		}
	}
	return n
}
