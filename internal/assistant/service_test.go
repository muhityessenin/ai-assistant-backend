package assistant

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestAssistantAccessQueryIsTenantScoped(t *testing.T) {
	b, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	q := string(b)
	for _, want := range []string{"a.organization_id=$1", "a.is_available_for_all_users", "aa.organization_id=$1", "aa.user_id=$4"} {
		if !strings.Contains(q, want) {
			t.Fatalf("assistant access lost invariant %q", want)
		}
	}
}
func TestAssistantSettingsAreBounded(t *testing.T) {
	raw, _ := json.Marshal(map[string]any{"rag": map[string]any{"enabled": true, "top_k": 10000, "min_score": -2}, "feedback": map[string]any{"enabled": true, "top_k": 1000}, "history": map[string]any{"message_limit": 10000}})
	s := (Assistant{Settings: raw}).ParsedSettings()
	if s.RAG.TopK != 50 || s.Feedback.TopK != 20 || s.History.MessageLimit != 100 || s.RAG.MinScore != 0 {
		t.Fatalf("unsafe settings were not bounded: %+v", s)
	}
}
