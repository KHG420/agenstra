// Package packfiles verifies immutable capability package content and local file layout.
package packfiles

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/KHG420/agenstra/internal/jsonvalue"
)

// Error identifies rejected file content or paths using a stable management code.
type Error struct{ Code string }

// Error returns the safe management identifier.
func (e *Error) Error() string    { return e.Code }
func fileError(code string) error { return &Error{Code: code} }

// Hash computes the canonical SHA-256 used by immutable package releases.
func Hash(v any) (string, error) {
	b, e := jsonvalue.Canonical(v)
	if e != nil {
		return "", e
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// SkillEntries validates relative skill paths and returns their declared digests.
func SkillEntries(manifest map[string]any) (map[string]string, error) {
	raw, ok := manifest["skills"]
	if !ok {
		return map[string]string{}, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, fileError("invalid_skills")
	}
	out := map[string]string{}
	for _, v := range items {
		item, ok := v.(map[string]any)
		if !ok {
			return nil, fileError("invalid_skills")
		}
		p, ok := item["path"].(string)
		if !ok {
			return nil, fileError("invalid_skills")
		}
		if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, `\`) || strings.HasPrefix(p, "pack.json/") || p == "pack.json" {
			return nil, fileError("invalid_skill_path")
		}
		for _, part := range strings.Split(p, "/") {
			if part == "" || part == "." || part == ".." {
				return nil, fileError("invalid_skill_path")
			}
		}
		if _, ok := out[p]; ok {
			return nil, fileError("duplicate_skill_path")
		}
		out[p], _ = item["sha256"].(string)
	}
	for p := range out {
		for q := range out {
			if p != q && strings.HasPrefix(q, p+"/") {
				return nil, fileError("skill_path_conflict")
			}
		}
	}
	return out, nil
}

// Verify checks a release directory against its expected manifest and complete content digest.
// The caller supplies validated release identifiers; Verify owns no registry or authorization state.
func Verify(packageDir, packID, digest, version, mj string) (string, error) {
	dir := filepath.Join(packageDir, packID, digest)
	path := filepath.Join(dir, "pack.json")
	for _, p := range []string{filepath.Join(packageDir, packID), dir} {
		info, err := os.Lstat(p)
		if err != nil || !info.IsDir() {
			return "", fileError("release_tampered")
		}
	}
	if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
		return "", fileError("release_tampered")
	}
	mb, e := os.ReadFile(path)
	if e != nil {
		return "", fileError("release_tampered")
	}
	if !json.Valid(mb) || !json.Valid([]byte(mj)) {
		return "", fileError("release_tampered")
	}
	var manifest, stored map[string]any
	manifestDecoder := json.NewDecoder(strings.NewReader(string(mb)))
	manifestDecoder.UseNumber()
	storedDecoder := json.NewDecoder(strings.NewReader(mj))
	storedDecoder.UseNumber()
	if manifestDecoder.Decode(&manifest) != nil || storedDecoder.Decode(&stored) != nil {
		return "", fileError("release_tampered")
	}
	b1, err := jsonvalue.Canonical(manifest)
	if err != nil {
		return "", fileError("release_tampered")
	}
	b2, err := jsonvalue.Canonical(stored)
	if err != nil {
		return "", fileError("release_tampered")
	}
	if string(b1) != string(b2) {
		return "", fileError("release_tampered")
	}
	entries, e := SkillEntries(manifest)
	if e != nil {
		return "", fileError("release_tampered")
	}
	allowed := map[string]bool{"pack.json": true}
	for p := range entries {
		allowed[p] = true
		parts := strings.Split(p, "/")
		for i := 1; i < len(parts); i++ {
			allowed[strings.Join(parts[:i], "/")] = true
		}
	}
	// Check entry types before reading skills, so recovery does not follow
	// symlinks or read special files from an uncommitted directory.
	e = filepath.WalkDir(dir, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if p == dir {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		_, file := entries[rel]
		file = file || rel == "pack.json"
		if !allowed[rel] || (file && !info.Mode().IsRegular()) || (!file && !info.IsDir()) {
			return fileError("release_tampered")
		}
		return nil
	})
	if e != nil {
		return "", fileError("release_tampered")
	}
	skills := map[string]string{}
	for p, want := range entries {
		data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if err != nil {
			return "", fileError("release_tampered")
		}
		hash := sha256.Sum256(data)
		if hex.EncodeToString(hash[:]) != want {
			return "", fileError("release_tampered")
		}
		skills[p] = string(data)
	}
	actual, err := Hash(map[string]any{"pack_id": packID, "version": version, "manifest": manifest, "skills": skills})
	if err != nil {
		return "", fileError("release_tampered")
	}
	if actual != digest {
		return "", fileError("release_tampered")
	}
	return path, nil
}
