package warnings

import "testing"

func TestWarningCodes(t *testing.T) {
	if CodeDurabilityUncertain == "" || CodeAuditWriteFailed == "" || CodeFileDeleteFailed == "" {
		t.Fatal("warning codes must be non-empty")
	}
	warning := Warning{Code: CodeDurabilityUncertain, Message: "fsync uncertain"}
	if warning.Code != "durability_uncertain" {
		t.Fatalf("warning = %#v", warning)
	}
}
