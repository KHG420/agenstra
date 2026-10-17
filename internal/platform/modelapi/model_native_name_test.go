package modelapi

import (
	"regexp"
	"strings"
	"testing"
)

func TestNativeFunctionBindingKeepsCompleteOperationName(t *testing.T) {
	names := []string{
		"first-project::admin.accounts.getTempUnschedulableStatus",
		"second-project::admin.accounts.getTempUnschedulableStatus",
		"first-project::user.accounts.getTempUnschedulableStatus",
	}
	seen := map[string]bool{}
	for _, name := range names {
		function := nativeCapabilityFunctionName(name)
		if !strings.HasPrefix(function, "cap_accounts_getTempUnschedulableStatus_") {
			t.Fatalf("binding truncates the operation while retaining redundant routing prefixes: %q", function)
		}
		if seen[function] || !regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`).MatchString(function) || nativeCapabilityFunctionName(name) != function {
			t.Fatal("request binding is ambiguous, invalid or unstable", function)
		}
		seen[function] = true
	}
}
