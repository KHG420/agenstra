package host

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	capabilitypack "github.com/KHG420/agenstra/internal/ext/capability"
)

func writeTestManifest(t *testing.T, v any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pack.json")
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLegacyFingerprintStability(t *testing.T) {
	manifest := `{"schema":"agenstra.capability-pack.v1","name":"review","guidance":"query records","capabilities":[{"name":"records.query","version":"1","description":"query","method":"GET","url_env":"REVIEW_URL","inputs":{"alpha":{"type":"string","description":"alpha"},"beta":{"type":"string","description":"beta"},"gamma":{"type":"string","description":"gamma"}},"outputs":{"alpha":{"type":"string","description":"alpha"},"beta":{"type":"string","description":"beta"},"gamma":{"type":"string","description":"gamma"}}}]}`
	path := filepath.Join(t.TempDir(), "pack.json")
	if err := os.WriteFile(path, []byte(manifest), 0600); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for range 60 {
		p, err := capabilitypack.LoadLegacyPack(path, map[string]string{"REVIEW_URL": "http://localhost/query"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		seen[Fingerprint(p)] = true
	}
	if len(seen) != 1 {
		t.Fatalf("same legacy manifest produced %d fingerprints", len(seen))
	}
}
