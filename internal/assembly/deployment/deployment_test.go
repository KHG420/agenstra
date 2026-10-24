package deployment

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestPostgresDeploymentSelection(t *testing.T) {
	base := `{"database":{"driver":"postgres","dsn_env":"TEST_DSN","schema":"agent_runs","web_schema":"agent_web"},"users":{}}`
	for _, test := range []struct {
		name    string
		replace func(string) string
		valid   bool
	}{
		{"postgres", func(s string) string { return s }, true},
		{"invalid_driver", func(s string) string { return strings.Replace(s, "postgres", "mysql", 1) }, false},
		{"invalid_schema", func(s string) string { return strings.Replace(s, "agent_runs", "public", 1) }, false},
		{"shared_schema", func(s string) string { return strings.Replace(s, "agent_web", "agent_runs", 1) }, false},
		{"invalid_reference", func(s string) string { return strings.Replace(s, "TEST_DSN", "password-in-json", 1) }, false},
		{"sqlite_conflict", func(s string) string {
			return strings.Replace(s, `"users":{}`, `"users":{},"database_path":"runs.sqlite3"`, 1)
		}, false},
		{"registry_rejected", func(s string) string { return strings.Replace(s, `"users":{}`, `"users":{},"management":{}`, 1) }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "deployment.json")
			if err := os.WriteFile(path, []byte(test.replace(base)), 0600); err != nil {
				t.Fatal(err)
			}
			dep, err := LoadDeployment(path)
			if (err == nil) != test.valid {
				t.Fatalf("valid=%v error=%v", test.valid, err)
			}
			if test.valid {
				if dep.Config.Database.Driver != "postgres" {
					t.Fatal("backend lost")
				}
				dep.Environment["TEST_DSN"] = ""
				if _, err = dep.OpenRunStore(); err == nil {
					t.Fatal("missing PostgreSQL DSN accepted")
				}
				// The parsed deployment remains an ordinary key/value JSON object.
				if _, err = json.Marshal(dep.Config); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

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
	if d.Config.Settings.MaxConcurrentRuns != 2 || d.Config.Settings.ModelTimeoutSeconds != agentcontract.DefaultHostSettings().ModelTimeoutSeconds {
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

func TestLoadDeploymentRequiresCompleteJSONDocument(t *testing.T) {
	valid := `{"database_path":"runs.sqlite3","users":{"alice":{"api_key_env":"ALICE_KEY"}},"settings":{"max_concurrent_runs":2}}`
	for _, tc := range []struct {
		name, suffix string
		wantError    bool
	}{
		{"whitespace", " \n\t", false},
		{"second deployment", "\n" + valid, true},
		{"second value", " null", true},
		{"trailing garbage", " incomplete", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "deployment.json")
			if err := os.WriteFile(path, []byte(valid+tc.suffix), 0600); err != nil {
				t.Fatal(err)
			}
			d, err := LoadDeployment(path)
			if tc.wantError {
				if err == nil || d != nil {
					t.Fatalf("incomplete deployment accepted: deployment=%v err=%v", d != nil, err)
				}
			} else if err != nil || d.Config.Settings.MaxConcurrentRuns != 2 || d.Config.Settings.ModelTimeoutSeconds != agentcontract.DefaultHostSettings().ModelTimeoutSeconds {
				t.Fatalf("valid deployment or partial defaults lost: deployment=%v err=%v", d, err)
			}
		})
	}
}
