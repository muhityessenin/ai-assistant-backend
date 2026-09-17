package conversation

import (
	"os"
	"strings"
	"testing"
)

func TestEmployeeConversationQueriesRequireTenantAndOwnership(t *testing.T) {
	b, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{"organization_id=$1", "($3 OR user_id=$4)", "m.organization_id=$1 AND m.conversation_id=$2", "mf.user_id=$3", "mode must be work or train"} {
		if !strings.Contains(src, want) {
			t.Fatalf("conversation isolation lost invariant %q", want)
		}
	}
}
