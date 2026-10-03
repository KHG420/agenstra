package agenstra

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// SkillFile pins a usage guide's path, metadata and SHA-256 digest.
type SkillFile struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	SHA256      string `json:"sha256"`
}

// LoadSkillFiles verifies pinned guides beneath the package directory and returns their contents.
func LoadSkillFiles(entries []SkillFile, directory string) (map[string]Skill, error) {
	root, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return nil, err
	}
	result := map[string]Skill{}
	for _, item := range entries {
		path, err := filepath.EvalSymlinks(filepath.Join(root, item.Path))
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return nil, errors.New("invalid skill path or duplicate name")
		}
		if _, ok := result[item.Name]; ok {
			return nil, errors.New("invalid skill path or duplicate name")
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != item.SHA256 {
			return nil, errors.New("skill content changed: " + item.Name)
		}
		if !utf8.Valid(b) {
			return nil, errors.New("invalid skill encoding")
		}
		result[item.Name] = Skill{Description: SkillDescription{Name: item.Name, Description: item.Description}, Content: string(b)}
	}
	return result, nil
}
