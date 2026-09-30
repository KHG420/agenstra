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
