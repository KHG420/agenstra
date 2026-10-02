package agenstra

import (
	"strings"
	"testing"
)

func TestBrowserContextDiagnosticsIdentifyPagePrerequisite(t *testing.T) {
	for _, code := range []string{"browser_context_required", "browser_context_changed"} {
		finding := ExplainRunError(code, "ui.read_activity")
		if finding.Category != "browser" || !strings.Contains(finding.NextAction, "ui.get_context") {
			t.Fatalf("misdirected page prerequisite: %+v", finding)
		}
	}
	if finding := ExplainRunError("browser_handler_outcome_unknown", "ui.finish_round"); finding.Category != "reconciliation" {
		t.Fatalf("uncertain writes must still require reconciliation: %+v", finding)
	}
}
