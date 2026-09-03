package knowledge

import (
	"os"
	"strings"
	"testing"
)

func TestRAGQueryIsTenantAndAssistantScoped(t *testing.T) {
	b, err := os.ReadFile("retriever.go")
	if err != nil {
		t.Fatal(err)
	}
	q := string(b)
	for _, required := range []string{"dc.organization_id=$1", "akb.assistant_id=$2", "akb.organization_id=dc.organization_id", "akb.knowledge_base_id=dc.knowledge_base_id"} {
		if !strings.Contains(q, required) {
			t.Fatalf("retrieval lost required scope: %s", required)
		}
	}
}
