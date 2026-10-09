package agent

import (
	"strings"
	"testing"
)

func TestProjectQualifiedDecisionNames(t *testing.T) {
	name := strings.Repeat("p", 128) + "::" + strings.Repeat("c", 128)
	decision := callDecision(name)
	if err := decision.Validate(); err != nil {
		t.Fatal("qualified valid names rejected", err)
	}
	if err := (Decision{Kind: "inspect_capability", Name: name}).Validate(); err != nil {
		t.Fatal("qualified inspection name rejected", err)
	}
	decision.Calls[0].Capability = strings.Repeat("x", 331)
	if err := decision.Validate(); err == nil {
		t.Fatal("unbounded qualified name accepted")
	}
}
