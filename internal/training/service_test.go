package training

import (
	"os"
	"strings"
	"testing"
)

func TestTrainingQueriesAreTenantAndAssistantScoped(t *testing.T) {
	b, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{"ate.organization_id=$1 AND ate.assistant_id=$2", "c.status='active'", "source_message_id"} {
		if !strings.Contains(src, want) {
			t.Fatalf("training isolation lost invariant %q", want)
		}
	}
}
