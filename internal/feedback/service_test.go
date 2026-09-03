package feedback

import (
	"os"
	"strings"
	"testing"
)

func TestLearnedRetrievalUsesOnlyApprovedTenantExamples(t *testing.T) {
	b, err := os.ReadFile("service.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{"organization_id=$1 AND assistant_id=$2 AND status='approved'", "m.organization_id=$1", "c.user_id=$4"} {
		if !strings.Contains(src, want) {
			t.Fatalf("feedback isolation lost invariant %q", want)
		}
	}
}
