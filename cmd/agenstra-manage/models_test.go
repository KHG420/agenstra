package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestModelCLIUsesRevisionAndPurpose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "models.json")
	if err := os.WriteFile(path, []byte(`{"default_profile":"business","profiles":{}}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		args          []string
		method, route string
		valid         bool
	}{
		{[]string{"list"}, "GET", "/admin/api/models", true},
		{[]string{"configure", path, "--revision", "3"}, "PUT", "/admin/api/models", true},
		{[]string{"check", "memory", "--purpose", "memory_extraction", "--config", path}, "POST", "/admin/api/models/check", true},
		{[]string{"configure", path}, "", "", false}, {[]string{"check", "business", "--purpose", "unknown"}, "", "", false},
	} {
		called := false
		_, err := modelCommand(tc.args, func(method, route string, body any) (any, error) {
			called = true
			if method != tc.method || route != tc.route {
				t.Fatal(method, route)
			}
			if method == "PUT" && body.(map[string]any)["expected_revision"] != 3 {
				t.Fatal("missing expected revision")
			}
			if method == "POST" && body.(map[string]any)["purpose"] != "memory_extraction" {
				t.Fatal("missing purpose")
			}
			return nil, nil
		})
		if (err == nil) != tc.valid || called != tc.valid {
			t.Fatal(tc.args, err, called)
		}
	}
}
