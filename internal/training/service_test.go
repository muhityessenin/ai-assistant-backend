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
	for _, want := range []string{"organization_id=$1 AND assistant_id=$2", "status='approved'", "scope='organization'", "scope='private' AND created_by=$3", "source_message_id", "TRAINING_FORBIDDEN"} {
		if !strings.Contains(src, want) {
			t.Fatalf("training isolation lost invariant %q", want)
		}
	}
}
