package agenstra

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadDeploymentDefaultsAndValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "deployment.json")
	valid := `{"database_path":"runs.sqlite3","users":{"alice":{"api_key_env":"ALICE_KEY"}},"settings":{"max_concurrent_runs":2}}`
	if e := os.WriteFile(path, []byte(valid), 0600); e != nil {
		t.Fatal(e)
	}
	d, e := LoadDeployment(path)
	if e != nil {
		t.Fatal(e)
	}
	if d.Config.Settings.MaxConcurrentRuns != 2 || d.Config.Settings.ModelTimeoutSeconds != DefaultHostSettings().ModelTimeoutSeconds {
		t.Fatalf("partial settings lost defaults: %+v", d.Config.Settings)
	}
	if d.DatabasePath() != filepath.Join(dir, "runs.sqlite3") {
		t.Fatalf("relative database path: %s", d.DatabasePath())
	}
	invalid := strings.Replace(valid, `"max_concurrent_runs":2`, `"max_concurrent_runs":0`, 1)
	if e = os.WriteFile(path, []byte(invalid), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = LoadDeployment(path); e == nil {
		t.Fatal("invalid settings accepted")
	}
	badAdmin := `{"database_path":"runs.sqlite3","users":{"alice":{"api_key_env":"ALICE_KEY"}},"management":{"database_path":"registry.sqlite3","package_dir":"packages","admin_api_key_env":"bad-name"}}`
	if e = os.WriteFile(path, []byte(badAdmin), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e = LoadDeployment(path); e == nil {
		t.Fatal("invalid management key reference accepted")
	}
}
