package agenstra

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

var registryID = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]{0,127}$`)
var registryDigest = regexp.MustCompile(`^[a-f0-9]{64}$`)

type RegistryError struct{ Code string }

func (e *RegistryError) Error() string { return e.Code }
func registryError(code string) error  { return &RegistryError{Code: code} }

type CapabilityRegistry struct {
	DatabasePath, PackageDir string
	db                       *sql.DB
}

func NewCapabilityRegistry(databasePath, packageDir string) *CapabilityRegistry {
	return &CapabilityRegistry{DatabasePath: databasePath, PackageDir: packageDir}
}
func (r *CapabilityRegistry) Initialize() error {
	if r.db != nil {
		return r.db.Ping()
	}
	a, _ := filepath.Abs(r.DatabasePath)
	b, _ := filepath.Abs(r.PackageDir)
	if a == b {
		return fmt.Errorf("registry database and package directory must differ")
	}
	if err := os.MkdirAll(filepath.Dir(r.DatabasePath), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(r.PackageDir, 0755); err != nil {
		return err
	}
	dsn := (&url.URL{Scheme: "file", Path: a}).String() + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(30000)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	for _, s := range []string{`PRAGMA busy_timeout=30000`, `PRAGMA foreign_keys=ON`, `PRAGMA journal_mode=WAL`,
		`CREATE TABLE IF NOT EXISTS releases (pack_id TEXT NOT NULL, digest TEXT NOT NULL, version TEXT NOT NULL, manifest_json TEXT NOT NULL, capabilities_json TEXT NOT NULL, created_at REAL NOT NULL, PRIMARY KEY(pack_id,digest), UNIQUE(pack_id,version))`,
		`CREATE TABLE IF NOT EXISTS active (pack_id TEXT PRIMARY KEY,digest TEXT NOT NULL,revision INTEGER NOT NULL,FOREIGN KEY(pack_id,digest) REFERENCES releases(pack_id,digest))`,
		`CREATE TABLE IF NOT EXISTS bindings (owner_id TEXT NOT NULL,pack_id TEXT NOT NULL,config_json TEXT NOT NULL,enabled INTEGER NOT NULL,updated_at REAL NOT NULL,PRIMARY KEY(owner_id,pack_id))`,
		`CREATE TABLE IF NOT EXISTS audit (sequence INTEGER PRIMARY KEY AUTOINCREMENT,created_at REAL NOT NULL,action TEXT NOT NULL,pack_id TEXT NOT NULL,detail_json TEXT NOT NULL)`} {
		if _, err = db.Exec(s); err != nil {
			db.Close()
			return err
		}
	}
	if _, err = db.Exec(`CREATE TABLE IF NOT EXISTS drafts (draft_id TEXT PRIMARY KEY, revision INTEGER NOT NULL, manifest_json TEXT NOT NULL, skills_json TEXT NOT NULL, updated_at REAL NOT NULL)`); err != nil {
		db.Close()
		return err
	}
	r.db = db
	return nil
}
func (r *CapabilityRegistry) Close() error {
	if r.db != nil {
		db := r.db
		r.db = nil
		return db.Close()
	}
	return nil
}
func registryCanonical(v any) ([]byte, error) { return CanonicalJSON(v) }
func registryHash(v any) (string, error) {
	b, e := registryCanonical(v)
	if e != nil {
		return "", e
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}
func registrySkillEntries(manifest map[string]any) (map[string]string, error) {
	raw, ok := manifest["skills"]
	if !ok {
		return map[string]string{}, nil
	}
	items, ok := raw.([]any)
	if !ok {
		return nil, registryError("invalid_skills")
	}
	out := map[string]string{}
	for _, v := range items {
		item, ok := v.(map[string]any)
		if !ok {
			return nil, registryError("invalid_skills")
		}
		p, ok := item["path"].(string)
		if !ok {
			return nil, registryError("invalid_skills")
		}
		if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, `\`) || strings.HasPrefix(p, "pack.json/") || p == "pack.json" {
			return nil, registryError("invalid_skill_path")
		}
		for _, part := range strings.Split(p, "/") {
			if part == "" || part == "." || part == ".." {
				return nil, registryError("invalid_skill_path")
			}
		}
		if _, ok := out[p]; ok {
			return nil, registryError("duplicate_skill_path")
		}
		out[p], _ = item["sha256"].(string)
	}
	for p := range out {
		for q := range out {
			if p != q && strings.HasPrefix(q, p+"/") {
				return nil, registryError("skill_path_conflict")
			}
		}
	}
	return out, nil
}
func registrySummary(manifest map[string]any) ([]map[string]string, error) {
	schema, _ := manifest["schema"].(string)
	key := "capabilities"
	if schema == "agenstra.mcp-pack.v1" {
		key = "tools"
	} else if schema != "agenstra.rest-pack.v2" && schema != "agenstra.capability-pack.v1" {
		return nil, registryError("unsupported_pack_schema")
	}
	items, ok := manifest[key].([]any)
	if !ok {
		return nil, registryError("invalid_capability_pack")
	}
	out := make([]map[string]string, 0, len(items))
	names := map[string]bool{}
	for _, v := range items {
		m, ok := v.(map[string]any)
		if !ok {
			return nil, registryError("invalid_capability_pack")
		}
		name, ok := m["name"].(string)
		if !ok || name == "" || names[name] {
			return nil, registryError("invalid_capability_pack")
		}
		names[name] = true
		effect := "compute"
		if schema != "agenstra.capability-pack.v1" {
			effect, ok = m["effect"].(string)
			if !ok {
				return nil, registryError("invalid_capability_pack")
			}
		}
		out = append(out, map[string]string{"name": name, "effect": effect})
	}
	return out, nil
}
func registryCheckPackage(packID, version string, manifest map[string]any, skills map[string]string) ([]map[string]string, error) {
	if !registryID.MatchString(packID) {
		return nil, registryError("invalid_pack_id")
	}
	if version == "" || len(version) > 40 || manifest["name"] != packID {
		return nil, registryError("pack_version_mismatch")
	}
	if mv, ok := manifest["version"]; ok && mv != version {
		return nil, registryError("pack_version_mismatch")
	}
	mb, e := registryCanonical(manifest)
	if e != nil {
		return nil, registryError("invalid_capability_pack")
	}
	n := 0
	for _, v := range skills {
		n += len(v)
	}
	if len(mb) > 1000000 || n > 1000000 {
		return nil, registryError("package_too_large")
	}
	entries, e := registrySkillEntries(manifest)
	if e != nil {
		return nil, e
	}
	if len(entries) != len(skills) {
		return nil, registryError("skill_files_mismatch")
	}
	for p, want := range entries {
		content, ok := skills[p]
		if !ok {
			return nil, registryError("skill_files_mismatch")
		}
		h := sha256.Sum256([]byte(content))
		if hex.EncodeToString(h[:]) != want {
			return nil, registryError("skill_digest_mismatch")
		}
	}
	summary, err := registrySummary(manifest)
	if err != nil {
		return nil, err
	}
	if err := ValidatePackManifest(manifest, skills); err != nil {
		return nil, registryError("invalid_capability_pack")
	}
	return summary, nil
}
func (r *CapabilityRegistry) Validate(packID, version string, manifest map[string]any, skills map[string]string) ([]map[string]string, error) {
	return registryCheckPackage(packID, version, manifest, skills)
}
func registryAudit(tx *sql.Tx, action, packID string, detail any) error {
	b, e := registryCanonical(detail)
	if e != nil {
		return e
	}
	_, e = tx.Exec(`INSERT INTO audit(created_at,action,pack_id,detail_json) VALUES(?,?,?,?)`, float64(time.Now().UnixNano())/1e9, action, packID, string(b))
	return e
}
func (r *CapabilityRegistry) Publish(packID, version string, manifest map[string]any, skills map[string]string) (map[string]any, error) {
	caps, e := r.Validate(packID, version, manifest, skills)
	if e != nil {
		return nil, e
	}
	digest, e := registryHash(map[string]any{"pack_id": packID, "version": version, "manifest": manifest, "skills": skills})
	if e != nil {
		return nil, e
	}
	var old string
	e = r.db.QueryRow(`SELECT digest FROM releases WHERE pack_id=? AND version=?`, packID, version).Scan(&old)
	if e == nil && old != digest {
		return nil, registryError("version_already_published")
	}
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	parent := filepath.Join(r.PackageDir, packID)
	if info, e := os.Lstat(parent); e == nil && info.Mode()&os.ModeSymlink != 0 {
		return nil, registryError("release_path_conflict")
	}
	if e := os.MkdirAll(parent, 0755); e != nil {
		return nil, e
	}
	target := filepath.Join(parent, digest)
	if _, e := os.Lstat(target); e == nil {
		if old == "" {
			return nil, registryError("release_path_conflict")
		}
		if _, e = r.ReleasePath(packID, digest); e != nil {
			return nil, e
		}
	} else if errors.Is(e, os.ErrNotExist) {
		stage, e := os.MkdirTemp(r.PackageDir, ".draft-")
		if e != nil {
			return nil, e
		}
		defer os.RemoveAll(stage)
		mb, _ := registryCanonical(manifest)
		if e := os.WriteFile(filepath.Join(stage, "pack.json"), mb, 0644); e != nil {
			return nil, e
		}
		for p, v := range skills {
			dst := filepath.Join(stage, filepath.FromSlash(p))
			if e := os.MkdirAll(filepath.Dir(dst), 0755); e != nil {
				return nil, e
			}
			if e := os.WriteFile(dst, []byte(v), 0644); e != nil {
				return nil, e
			}
		}
		if e := os.Rename(stage, target); e != nil {
			return nil, e
		}
	}
	tx, e := r.db.Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	now := float64(time.Now().UnixNano()) / 1e9
	var created float64
	e = tx.QueryRow(`SELECT digest,created_at FROM releases WHERE pack_id=? AND version=?`, packID, version).Scan(&old, &created)
	if e == nil {
		if old != digest {
			return nil, registryError("version_already_published")
		}
	} else if errors.Is(e, sql.ErrNoRows) {
		mb, _ := registryCanonical(manifest)
		cb, _ := registryCanonical(caps)
		if _, e = tx.Exec(`INSERT INTO releases VALUES(?,?,?,?,?,?)`, packID, digest, version, string(mb), string(cb), now); e != nil {
			return nil, e
		}
		if e = registryAudit(tx, "publish", packID, map[string]any{"version": version, "digest": digest}); e != nil {
			return nil, e
		}
		created = now
	} else {
		return nil, e
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return map[string]any{"pack_id": packID, "version": version, "digest": digest, "capabilities": caps, "created_at": created}, nil
}
func (r *CapabilityRegistry) ActiveRelease(packID string) (string, error) {
	if !registryID.MatchString(packID) {
		return "", registryError("invalid_pack_id")
	}
	var digest string
	e := r.db.QueryRow(`SELECT digest FROM active WHERE pack_id=?`, packID).Scan(&digest)
	if errors.Is(e, sql.ErrNoRows) {
		return "", nil
	}
	return digest, e
}
func (r *CapabilityRegistry) ReleasePath(packID, digest string) (string, error) {
	if !registryID.MatchString(packID) {
		return "", registryError("invalid_pack_id")
	}
	if !registryDigest.MatchString(digest) {
		return "", registryError("invalid_release_id")
	}
	var version, mj string
	e := r.db.QueryRow(`SELECT version,manifest_json FROM releases WHERE pack_id=? AND digest=?`, packID, digest).Scan(&version, &mj)
	if errors.Is(e, sql.ErrNoRows) {
		return "", registryError("release_not_found")
	}
	if e != nil {
		return "", e
	}
	dir := filepath.Join(r.PackageDir, packID, digest)
	path := filepath.Join(dir, "pack.json")
	for _, p := range []string{filepath.Join(r.PackageDir, packID), dir, path} {
		info, e := os.Lstat(p)
		if e != nil || info.Mode()&os.ModeSymlink != 0 {
			return "", registryError("release_tampered")
		}
	}
	mb, e := os.ReadFile(path)
	if e != nil {
		return "", registryError("release_tampered")
	}
	var manifest, stored map[string]any
	manifestDecoder := json.NewDecoder(strings.NewReader(string(mb)))
	manifestDecoder.UseNumber()
	storedDecoder := json.NewDecoder(strings.NewReader(mj))
	storedDecoder.UseNumber()
	if manifestDecoder.Decode(&manifest) != nil || storedDecoder.Decode(&stored) != nil {
		return "", registryError("release_tampered")
	}
	b1, _ := registryCanonical(manifest)
	b2, _ := registryCanonical(stored)
	if string(b1) != string(b2) {
		return "", registryError("release_tampered")
	}
	entries, e := registrySkillEntries(manifest)
	if e != nil {
		return "", registryError("release_tampered")
	}
	allowed := map[string]bool{"pack.json": true}
	skills := map[string]string{}
	for p, want := range entries {
		allowed[p] = true
		parts := strings.Split(p, "/")
		for i := 1; i < len(parts); i++ {
			allowed[strings.Join(parts[:i], "/")] = true
		}
		data, e := os.ReadFile(filepath.Join(dir, filepath.FromSlash(p)))
		if e != nil {
			return "", registryError("release_tampered")
		}
		h := sha256.Sum256(data)
		if hex.EncodeToString(h[:]) != want {
			return "", registryError("release_tampered")
		}
		skills[p] = string(data)
	}
	e = filepath.WalkDir(dir, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if p == dir {
			return nil
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if !allowed[rel] || d.Type()&os.ModeSymlink != 0 {
			return registryError("release_tampered")
		}
		return nil
	})
	if e != nil {
		return "", registryError("release_tampered")
	}
	actual, _ := registryHash(map[string]any{"pack_id": packID, "version": version, "manifest": manifest, "skills": skills})
	if actual != digest {
		return "", registryError("release_tampered")
	}
	return path, nil
}
func (r *CapabilityRegistry) Activate(packID, digest string, expectedRevision *int) (map[string]any, error) {
	if _, e := r.ReleasePath(packID, digest); e != nil {
		return nil, e
	}
	if expectedRevision != nil && *expectedRevision < 0 {
		return nil, registryError("invalid_revision")
	}
	tx, e := r.db.Begin()
	if e != nil {
		return nil, e
	}
	defer tx.Rollback()
	var version, cj string
	e = tx.QueryRow(`SELECT version,capabilities_json FROM releases WHERE pack_id=? AND digest=?`, packID, digest).Scan(&version, &cj)
	if e != nil {
		return nil, registryError("release_not_found")
	}
	var caps []map[string]string
	json.Unmarshal([]byte(cj), &caps)
	names := map[string]bool{}
	for _, c := range caps {
		names[c["name"]] = true
	}
	rows, e := tx.Query(`SELECT config_json FROM bindings WHERE pack_id=? AND enabled=1`, packID)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var s string
		rows.Scan(&s)
		var c map[string]any
		json.Unmarshal([]byte(s), &c)
		for _, k := range []string{"granted_capabilities", "approval_capabilities"} {
			if arr, ok := c[k].([]any); ok {
				for _, v := range arr {
					if !names[fmt.Sprint(v)] {
						rows.Close()
						return nil, registryError("binding_capability_missing")
					}
				}
			}
		}
	}
	rows.Close()
	var prior string
	revision := 0
	e = tx.QueryRow(`SELECT digest,revision FROM active WHERE pack_id=?`, packID).Scan(&prior, &revision)
	if e != nil && !errors.Is(e, sql.ErrNoRows) {
		return nil, e
	}
	if expectedRevision != nil && revision != *expectedRevision {
		return nil, registryError("revision_conflict")
	}
	if prior != digest {
		revision++
		if _, e = tx.Exec(`INSERT INTO active(pack_id,digest,revision) VALUES(?,?,?) ON CONFLICT(pack_id) DO UPDATE SET digest=excluded.digest,revision=excluded.revision`, packID, digest, revision); e != nil {
			return nil, e
		}
		if e = registryAudit(tx, "activate", packID, map[string]any{"digest": digest, "revision": revision}); e != nil {
			return nil, e
		}
	}
	if e = tx.Commit(); e != nil {
		return nil, e
	}
	return map[string]any{"pack_id": packID, "version": version, "digest": digest, "revision": revision}, nil
}
func (r *CapabilityRegistry) PutBinding(ownerID, packID string, config map[string]any) error {
	if !registryID.MatchString(packID) {
		return registryError("invalid_pack_id")
	}
	if ownerID == "" || len(ownerID) > 128 {
		return registryError("invalid_owner_id")
	}
	tx, e := r.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var cj string
	e = tx.QueryRow(`SELECT r.capabilities_json FROM active a JOIN releases r ON r.pack_id=a.pack_id AND r.digest=a.digest WHERE a.pack_id=?`, packID).Scan(&cj)
	if errors.Is(e, sql.ErrNoRows) {
		return registryError("release_not_active")
	}
	if e != nil {
		return e
	}
	var caps []map[string]string
	json.Unmarshal([]byte(cj), &caps)
	names := map[string]bool{}
	for _, c := range caps {
		names[c["name"]] = true
	}
	for _, k := range []string{"granted_capabilities", "approval_capabilities"} {
		if arr, ok := config[k].([]any); ok {
			for _, v := range arr {
				if !names[fmt.Sprint(v)] {
					return registryError("binding_capability_missing")
				}
			}
		}
	}
	b, e := registryCanonical(config)
	if e != nil {
		return e
	}
	_, e = tx.Exec(`INSERT INTO bindings VALUES(?,?,?,?,?) ON CONFLICT(owner_id,pack_id) DO UPDATE SET config_json=excluded.config_json,enabled=1,updated_at=excluded.updated_at`, ownerID, packID, string(b), 1, float64(time.Now().UnixNano())/1e9)
	if e != nil {
		return e
	}
	if e = registryAudit(tx, "bind", packID, map[string]any{"owner_id": ownerID}); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *CapabilityRegistry) DisableBinding(ownerID, packID string) error {
	tx, e := r.db.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	res, e := tx.Exec(`UPDATE bindings SET enabled=0,updated_at=? WHERE owner_id=? AND pack_id=?`, float64(time.Now().UnixNano())/1e9, ownerID, packID)
	if e != nil {
		return e
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return registryError("binding_not_found")
	}
	if e = registryAudit(tx, "disable_binding", packID, map[string]any{"owner_id": ownerID}); e != nil {
		return e
	}
	return tx.Commit()
}
func (r *CapabilityRegistry) Binding(ownerID, packID string) (bool, map[string]any, error) {
	var cj string
	var enabled int
	e := r.db.QueryRow(`SELECT config_json,enabled FROM bindings WHERE owner_id=? AND pack_id=?`, ownerID, packID).Scan(&cj, &enabled)
	if errors.Is(e, sql.ErrNoRows) {
		return false, nil, nil
	}
	if e != nil {
		return false, nil, e
	}
	if enabled == 0 {
		return true, nil, nil
	}
	var c map[string]any
	e = json.Unmarshal([]byte(cj), &c)
	return true, c, e
}
func (r *CapabilityRegistry) ListPacks() ([]map[string]any, error) {
	rows, e := r.db.Query(`SELECT r.pack_id,r.version,r.digest,r.capabilities_json,r.created_at,a.digest,a.revision FROM releases r LEFT JOIN active a ON a.pack_id=r.pack_id ORDER BY r.pack_id,r.created_at DESC`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, v, d, cj string
		var created float64
		var active sql.NullString
		var rev sql.NullInt64
		if e = rows.Scan(&id, &v, &d, &cj, &created, &active, &rev); e != nil {
			return nil, e
		}
		var caps any
		json.Unmarshal([]byte(cj), &caps)
		out = append(out, map[string]any{"pack_id": id, "version": v, "digest": d, "capabilities": caps, "created_at": created, "active": active.Valid && active.String == d, "revision": rev.Int64})
	}
	return out, rows.Err()
}
func (r *CapabilityRegistry) ListBindings() ([]map[string]any, error) {
	rows, e := r.db.Query(`SELECT owner_id,pack_id,config_json,enabled,updated_at FROM bindings ORDER BY owner_id,pack_id`)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var owner, pack, cj string
		var enabled int
		var updated float64
		if e = rows.Scan(&owner, &pack, &cj, &enabled, &updated); e != nil {
			return nil, e
		}
		var config any
		json.Unmarshal([]byte(cj), &config)
		out = append(out, map[string]any{"owner_id": owner, "pack_id": pack, "config": config, "enabled": enabled != 0, "updated_at": updated})
	}
	return out, rows.Err()
}
func (r *CapabilityRegistry) Audit(limit int) ([]map[string]any, error) {
	if limit < 1 || limit > 1000 {
		return nil, registryError("invalid_limit")
	}
	rows, e := r.db.Query(`SELECT sequence,created_at,action,pack_id,detail_json FROM audit ORDER BY sequence DESC LIMIT ?`, limit)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var seq int64
		var at float64
		var action, pack, dj string
		if e = rows.Scan(&seq, &at, &action, &pack, &dj); e != nil {
			return nil, e
		}
		var detail any
		json.Unmarshal([]byte(dj), &detail)
		out = append(out, map[string]any{"sequence": seq, "created_at": at, "action": action, "pack_id": pack, "detail": detail})
	}
	return out, rows.Err()
}
func registryNames(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
