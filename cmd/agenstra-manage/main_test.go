package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPackageRejectsSymlinkEscapingPack(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if e := os.WriteFile(filepath.Join(outside, "SKILL.md"), []byte("secret"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Symlink(filepath.Join(outside, "SKILL.md"), filepath.Join(root, "SKILL.md")); e != nil {
		t.Fatal(e)
	}
	manifest := `{"name":"records","version":"1.0.0","skills":[{"path":"SKILL.md"}]}`
	path := filepath.Join(root, "pack.json")
	if e := os.WriteFile(path, []byte(manifest), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := packageBody(path, ""); e == nil {
		t.Fatal("symlink outside pack was read")
	}
}

func TestPackageRequiresCompleteJSONObject(t *testing.T) {
	for _, tc := range []struct {
		name, suffix string
		wantError    bool
	}{
		{"whitespace", " \n\t", false},
		{"second value", " {}", true},
		{"garbage", " unfinished", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "pack.json")
			if err := os.WriteFile(path, []byte(`{"name":"records","version":"1","skills":[]}`+tc.suffix), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := packageBody(path, "")
			if (err != nil) != tc.wantError {
				t.Fatalf("packageBody error = %v, wantError = %v", err, tc.wantError)
			}
		})
	}
	path := filepath.Join(t.TempDir(), "pack.json")
	if err := os.WriteFile(path, []byte("null"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := packageBody(path, ""); err == nil {
		t.Fatal("null manifest accepted")
	}
}
