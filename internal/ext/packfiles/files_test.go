package packfiles

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
)

func TestSkillPathsRemainInsideTheManifestDirectory(t *testing.T) {
	for _, path := range []string{"", "/guide.md", "../guide.md", "guides/../guide.md", `guides\guide.md`, "pack.json", "pack.json/guide.md", "guides//guide.md"} {
		_, err := SkillEntries(map[string]any{"skills": []any{map[string]any{"path": path}}})
		var failure *Error
		if !errors.As(err, &failure) || failure.Code != "invalid_skill_path" {
			t.Fatal(path, err)
		}
	}
	_, err := SkillEntries(map[string]any{"skills": []any{map[string]any{"path": "guides"}, map[string]any{"path": "guides/read.md"}}})
	if err == nil {
		t.Fatal("file-directory collision accepted")
	}
	entries, err := SkillEntries(map[string]any{"skills": []any{map[string]any{"path": "guides/read.md", "sha256": "digest"}}})
	if err != nil || entries["guides/read.md"] != "digest" {
		t.Fatal(entries, err)
	}
}

func TestVerifyRejectsUnexpectedEntryTypes(t *testing.T) {
	for _, change := range []string{"unchanged", "extra", "symlink directory", "symlink file"} {
		t.Run(change, func(t *testing.T) {
			packageDir := t.TempDir()
			content := "Read current records"
			// Hash raw skill bytes using the same SHA-256 expected by the manifest.
			sum := sha256.Sum256([]byte(content))
			manifest := map[string]any{"skills": []any{map[string]any{"path": "guides/read.md", "sha256": hex.EncodeToString(sum[:])}}}
			digest, err := Hash(map[string]any{"pack_id": "records", "version": "1", "manifest": manifest, "skills": map[string]string{"guides/read.md": content}})
			if err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(packageDir, "records", digest)
			if err := os.MkdirAll(filepath.Join(dir, "guides"), 0755); err != nil {
				t.Fatal(err)
			}
			raw, err := jsonvalue.Canonical(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "pack.json"), raw, 0644); err != nil {
				t.Fatal(err)
			}
			guide := filepath.Join(dir, "guides", "read.md")
			if err := os.WriteFile(guide, []byte(content), 0644); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "extra":
				err = os.WriteFile(filepath.Join(dir, "extra"), nil, 0644)
			case "symlink directory":
				backup := filepath.Join(packageDir, "outside")
				err = os.Rename(filepath.Join(dir, "guides"), backup)
				if err == nil {
					err = os.Symlink(backup, filepath.Join(dir, "guides"))
				}
			case "symlink file":
				backup := filepath.Join(packageDir, "outside.md")
				err = os.Rename(guide, backup)
				if err == nil {
					err = os.Symlink(backup, guide)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			path, err := Verify(packageDir, "records", digest, "1", string(raw))
			if change == "unchanged" {
				if err != nil || path != filepath.Join(dir, "pack.json") {
					t.Fatal(path, err)
				}
			} else {
				var failure *Error
				if !errors.As(err, &failure) || failure.Code != "release_tampered" {
					t.Fatal(path, err)
				}
			}
		})
	}
}
