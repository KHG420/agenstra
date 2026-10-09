package registry

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/KHG420/agenstra/internal/base/jsonvalue"
	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	capabilitypack "github.com/KHG420/agenstra/internal/ext/capability"
)

func draftKey(m map[string]any) string {
	if m["schema"] == "agenstra.mcp-pack.v1" {
		return "tools"
	}
	return "capabilities"
}

// DraftString reads an optional string field from a draft document.
func DraftString(m map[string]any, k string) string { s, _ := m[k].(string); return s }

func skillDigest(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// Report incomplete fields independently; the final check uses the same validator as publishing.
func draftIssues(m map[string]any, skills map[string]string) []agentcontract.DraftIssue {
	issues := []agentcontract.DraftIssue{}
	add := func(section, path, message string) {
		issues = append(issues, agentcontract.DraftIssue{Section: section, Path: path, Message: message})
	}
	for _, field := range []string{"name", "version", "guidance"} {
		if strings.TrimSpace(DraftString(m, field)) == "" {
			add("basic", field, "请填写此字段。")
		}
	}
	if m["schema"] == "agenstra.rest-pack.v2" {
		if !capabilitypack.EnvPattern.MatchString(DraftString(m, "base_url_env")) {
			add("connection", "base_url_env", "请填写接口地址的环境变量名，例如 RECORDS_API_URL。")
		}
	} else if m["schema"] == "agenstra.mcp-pack.v1" {
		source, _ := m["source"].(map[string]any)
		transport := DraftString(source, "transport")
		if transport != "stdio" && transport != "streamable_http" {
			add("connection", "source.transport", "请选择 stdio 或 streamable_http。")
		} else if transport == "stdio" && DraftString(source, "command") == "" {
			add("connection", "source.command", "请填写启动命令。")
		} else if transport == "streamable_http" && !capabilitypack.EnvPattern.MatchString(DraftString(source, "url_env")) {
			add("connection", "source.url_env", "请填写 MCP 地址的环境变量名。")
		}
	}
	key := draftKey(m)
	items, _ := m[key].([]any)
	if len(items) == 0 {
		add("capabilities", key, "请至少添加一项能力。")
	}

	// A valid envelope isolates errors to each capability, even while other steps are incomplete.
	envelope := map[string]any{"schema": m["schema"], "name": "draft", "version": "1", "guidance": "Draft validation"}
	if key == "tools" {
		envelope["source"] = map[string]any{"transport": "stdio", "command": "draft"}
	} else {
		envelope["base_url_env"] = "DRAFT_API_URL"
		envelope["headers_env"] = m["headers_env"]
		envelope["token_env"] = m["token_env"]
	}
	seen := map[string]bool{}
	for i, item := range items {
		obj, ok := item.(map[string]any)
		path := fmt.Sprintf("%s[%d]", key, i)
		if !ok {
			add("capabilities", path, "能力必须是 JSON 对象。")
			continue
		}
		name := DraftString(obj, "name")
		if name == "" || seen[name] {
			add("capabilities", path+".name", "请填写唯一的能力名称。")
			continue
		}
		seen[name] = true
		before := len(issues)
		section := "capabilities"
		if !map[string]bool{"read": true, "compute": true, "write": true, "destructive": true}[DraftString(obj, "effect")] {
			add("rules", path+".effect", "请选择读取、计算、写入或破坏性操作。")
		}
		if key == "capabilities" {
			if DraftString(obj, "description") == "" {
				add(section, path+".description", "请说明何时使用这项能力。")
			}
			if DraftString(obj, "path") == "" {
				add(section, path+".path", "请填写接口路径。")
			}
			for _, field := range []string{"input_schema", "output_schema"} {
				schema, ok := obj[field].(map[string]any)
				if !ok {
					add(section, path+"."+field, "请填写 JSON Schema 对象。")
				} else if _, err := agentcontract.ValidateLocalSchema(schema, true); err != nil {
					add(section, path+"."+field, err.Error())
				}
			}
		} else if !capabilitypack.SafeSHA256(DraftString(obj, "contract_sha256")) {
			add(section, path+".contract_sha256", "请填写已审查完整工具契约的 64 位 SHA-256。")
		}

		// Skill completeness belongs to the skill step, independently of contracts.
		copy := map[string]any{}
		for field, v := range obj {
			if field != "skills" {
				copy[field] = v
			}
		}
		declared := map[string]bool{}
		entries, _ := m["skills"].([]any)
		for _, entry := range entries {
			skill, _ := entry.(map[string]any)
			declared[DraftString(skill, "name")] = true
		}
		if refs, ok := obj["skills"].([]any); ok {
			for _, ref := range refs {
				name, ok := ref.(string)
				if !ok || !declared[name] {
					add(section, path+".skills", "关联的技能名称不存在，请在使用说明步骤添加。")
				}
			}
		} else if obj["skills"] != nil {
			add(section, path+".skills", "关联技能必须是名称数组。")
		}
		envelope[key] = []any{copy}
		if before == len(issues) {
			if err := capabilitypack.ValidatePackManifest(envelope, map[string]string{}); err != nil {
				message := err.Error()
				field := ""
				for _, candidate := range []string{"timeout_seconds", "replay", "reference_scope", "idempotency", "effect"} {
					if strings.Contains(message, candidate) {
						section = "rules"
						field = "." + candidate
						break
					}
				}
				if strings.Contains(message, "path") {
					field = ".path"
				}
				add(section, path+field+" ("+name+")", message)
			}
		}
	}
	if entries, err := registrySkillEntries(m); err != nil {
		add("skills", "skills", err.Error())
	} else {
		for path, hash := range entries {
			content, ok := skills[path]
			if !ok {
				add("skills", path, "请添加对应的技能文件。")
			} else if capabilitypack.SafeSHA256(hash) && hash != skillDigest(content) {
				add("skills", path, "技能内容与 SHA-256 不一致，请更新哈希。")
			}
		}
		for path := range skills {
			if _, ok := entries[path]; !ok {
				add("skills", path, "请为此技能文件添加清单声明。")
			}
		}
	}
	if len(issues) == 0 {
		if _, err := registryCheckPackage(DraftString(m, "name"), DraftString(m, "version"), m, skills); err != nil {
			message := err.Error()
			if e := capabilitypack.ValidatePackManifest(m, skills); e != nil {
				message = e.Error()
			}
			section, path := "review", "manifest"
			for _, field := range []string{"token_env", "headers_env", "source"} {
				if strings.Contains(message, field) {
					section = "connection"
					path = field
					break
				}
			}
			add(section, path, message)
		}
	}
	return issues
}

// Draft reads an independent editable manifest and its current management revision.
func (r *CapabilityRegistry) Draft(id string) (*agentcontract.CapabilityDraft, error) {
	if !agentcontract.RegistryID.MatchString(id) {
		return nil, agentcontract.NewRegistryError("invalid_draft_id")
	}
	d := &agentcontract.CapabilityDraft{DraftID: id}
	var m, s string
	err := r.db.QueryRow(`SELECT revision,manifest_json,skills_json,updated_at FROM drafts WHERE draft_id=?`, id).Scan(&d.Revision, &m, &s, &d.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, agentcontract.NewRegistryError("draft_not_found")
	}
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(strings.NewReader(m))
	dec.UseNumber()
	if err = dec.Decode(&d.Manifest); err != nil {
		return nil, err
	}
	if err = json.Unmarshal([]byte(s), &d.Skills); err != nil {
		return nil, err
	}
	d.Issues = draftIssues(d.Manifest, d.Skills)
	return d, nil
}

// ListDrafts returns saved draft summaries without opening business connections.
func (r *CapabilityRegistry) ListDrafts() (result []map[string]any, resultErr error) {
	rows, err := r.db.Query(`SELECT draft_id,revision,manifest_json,updated_at FROM drafts ORDER BY updated_at DESC,draft_id`)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, rows.Close()) }()
	out := []map[string]any{}
	for rows.Next() {
		var id, m string
		var revision int
		var updated float64
		if err = rows.Scan(&id, &revision, &m, &updated); err != nil {
			return nil, err
		}
		var manifest map[string]any
		if err = json.Unmarshal([]byte(m), &manifest); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"draft_id": id, "revision": revision, "name": manifest["name"], "version": manifest["version"], "updated_at": updated})
	}
	return out, rows.Err()
}

// SaveDraft validates serialization and saves a draft under its expected revision.
func (r *CapabilityRegistry) SaveDraft(id string, expected *int, m map[string]any, skills map[string]string) (output *agentcontract.CapabilityDraft, resultErr error) {
	if !agentcontract.RegistryID.MatchString(id) {
		return nil, agentcontract.NewRegistryError("invalid_draft_id")
	}
	if expected == nil || *expected < 0 {
		return nil, agentcontract.NewRegistryError("invalid_revision")
	}
	if m == nil || (m["schema"] != "agenstra.rest-pack.v2" && m["schema"] != "agenstra.mcp-pack.v1") {
		return nil, agentcontract.NewRegistryError("unsupported_pack_schema")
	}
	if skills == nil {
		skills = map[string]string{}
	}
	mb, err := RegistryCanonical(m)
	if err != nil {
		return nil, err
	}
	n := 0
	for _, v := range skills {
		n += len(v)
	}
	if len(mb) > 1000000 || n > 1000000 {
		return nil, agentcontract.NewRegistryError("package_too_large")
	}

	// Validate paths even for incomplete drafts, since drafts can be exported later.
	if _, err = registrySkillEntries(m); err != nil {
		return nil, err
	}
	for p := range skills {
		if _, err = registrySkillEntries(map[string]any{"skills": []any{map[string]any{"path": p}}}); err != nil {
			return nil, err
		}
	}
	sb, err := RegistryCanonical(skills)
	if err != nil {
		return nil, err
	}
	now := float64(time.Now().UnixNano()) / 1e9
	tx, err := r.db.Begin()
	if err != nil {
		return nil, err
	}
	defer func() {

		// A committed transaction is already closed; other rollback failures remain visible.
		if rollbackErr := tx.Rollback(); !errors.Is(rollbackErr, sql.ErrTxDone) {
			resultErr = errors.Join(resultErr, rollbackErr)
		}
	}()
	var result sql.Result
	if *expected == 0 {
		result, err = tx.Exec(`INSERT INTO drafts VALUES(?,1,?,?,?) ON CONFLICT(draft_id) DO NOTHING`, id, string(mb), string(sb), now)
	} else {
		result, err = tx.Exec(`UPDATE drafts SET revision=revision+1,manifest_json=?,skills_json=?,updated_at=? WHERE draft_id=? AND revision=?`, string(mb), string(sb), now, id, *expected)
	}
	if err != nil {
		return nil, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if count != 1 {
		return nil, agentcontract.NewRegistryError("draft_revision_conflict")
	}
	if err = registryAudit(tx, "save_draft", DraftString(m, "name"), map[string]any{"draft_id": id, "revision": *expected + 1}); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}

	// Return the exact saved snapshot, not a later concurrent revision.
	return &agentcontract.CapabilityDraft{DraftID: id, Revision: *expected + 1, Manifest: m, Skills: skills, UpdatedAt: now, Issues: draftIssues(m, skills)}, nil
}

func mergeDraftItems(existing, incoming []any, conflict string) ([]any, error) {
	if conflict == "" {
		conflict = "error"
	}
	if conflict != "error" && conflict != "keep" && conflict != "replace" {
		return nil, agentcontract.NewRegistryError("invalid_conflict_policy")
	}
	out := append([]any{}, existing...)
	index := map[string]int{}
	for i, item := range out {
		m, ok := item.(map[string]any)
		if !ok || DraftString(m, "name") == "" {
			return nil, agentcontract.NewRegistryError("invalid_draft_item")
		}
		name := DraftString(m, "name")
		if _, ok := index[name]; ok {
			return nil, agentcontract.NewRegistryError("invalid_draft_item")
		}
		index[name] = i
	}
	seen := map[string]bool{}
	for _, item := range incoming {
		m, ok := item.(map[string]any)
		if !ok || DraftString(m, "name") == "" {
			return nil, agentcontract.NewRegistryError("invalid_draft_item")
		}
		name := DraftString(m, "name")
		if seen[name] {
			return nil, agentcontract.NewRegistryError("invalid_draft_item")
		}
		seen[name] = true
		if i, exists := index[name]; exists {
			if conflict == "error" {
				return nil, agentcontract.NewRegistryError("draft_item_conflict")
			}
			if conflict == "replace" {
				out[i] = item
			}
		} else {
			index[name] = len(out)
			out = append(out, item)
		}
	}
	return out, nil
}

// EditDraft applies one supported edit under the draft's expected revision.
func (r *CapabilityRegistry) EditDraft(id string, edit agentcontract.DraftEdit) (*agentcontract.CapabilityDraft, error) {
	d, err := r.Draft(id)
	if err != nil {
		return nil, err
	}
	if edit.ExpectedRevision == nil || *edit.ExpectedRevision != d.Revision {
		return nil, agentcontract.NewRegistryError("draft_revision_conflict")
	}
	m := d.Manifest
	key := draftKey(m)
	switch edit.Section {
	case "basic", "connection":
		allowed := map[string]bool{"name": true, "version": true, "guidance": true}
		if edit.Section == "connection" {
			allowed = map[string]bool{"base_url_env": true, "token_env": true, "headers_env": true}
			if key == "tools" {
				allowed = map[string]bool{"source": true, "error_code_path": true}
			}
		}
		for k, v := range edit.Value {
			if !allowed[k] {
				return nil, agentcontract.NewRegistryError("invalid_draft_field")
			}
			if v == nil {
				delete(m, k)
			} else {
				m[k] = v
			}
		}
	case "capabilities":
		existing, _ := m[key].([]any)
		m[key], err = mergeDraftItems(existing, edit.Items, edit.Conflict)
	case "import", "openapi":
		incoming := edit.Value
		if edit.Section == "openapi" {
			if key != "capabilities" {
				return nil, agentcontract.NewRegistryError("unsupported_pack_schema")
			}
			raw, encodeErr := json.Marshal(edit.Value)
			if encodeErr != nil {
				return nil, agentcontract.NewRegistryError("invalid_request")
			}
			var args struct {
				Spec       agentcontract.JSON `json:"spec"`
				Operations []string           `json:"operations"`
				Effects    map[string]string  `json:"effects"`
			}
			if err = jsonvalue.DecodeStrict(raw, &args); err != nil {
				return nil, agentcontract.NewRegistryError("invalid_request")
			}
			incoming, err = capabilitypack.ImportOpenAPIDocument(args.Spec, DraftString(m, "name"), DraftString(m, "base_url_env"), args.Operations, args.Effects, DraftString(m, "token_env"))
			if err != nil {
				return nil, agentcontract.NewRegistryError("openapi_import_failed")
			}
		}
		if incoming["schema"] != m["schema"] {
			return nil, agentcontract.NewRegistryError("unsupported_pack_schema")
		}
		existing, _ := m[key].([]any)
		items, ok := incoming[key].([]any)
		if !ok {
			return nil, agentcontract.NewRegistryError("invalid_draft_item")
		}
		m[key], err = mergeDraftItems(existing, items, edit.Conflict)
		if err != nil {
			return nil, err
		}
		entries, _ := incoming["skills"].([]any)
		edit.Items = entries
		if err = mergeDraftSkills(d, edit); err != nil {
			return nil, err
		}
	case "skills":
		err = mergeDraftSkills(d, edit)
	case "remove_capability", "remove_skill":
		field := key
		if edit.Section == "remove_skill" {
			field = "skills"
		}
		items, _ := m[field].([]any)
		out := []any{}
		for _, item := range items {
			obj, _ := item.(map[string]any)
			if DraftString(obj, "name") == edit.Name {
				if field == "skills" {
					delete(d.Skills, DraftString(obj, "path"))
				}
			} else {
				out = append(out, item)
			}
		}
		m[field] = out
	default:
		return nil, agentcontract.NewRegistryError("invalid_draft_section")
	}
	if err != nil {
		return nil, err
	}
	return r.SaveDraft(id, edit.ExpectedRevision, m, d.Skills)
}

func mergeDraftSkills(d *agentcontract.CapabilityDraft, edit agentcontract.DraftEdit) error {
	old, _ := d.Manifest["skills"].([]any)
	merged, err := mergeDraftItems(old, edit.Items, edit.Conflict)
	if err != nil {
		return err
	}

	// Preserve bytes for kept entries; replacing a path removes its old bytes.
	contents := map[string]string{}
	for path, content := range d.Skills {
		contents[path] = content
	}

	// Only explicit replacement of a declared skill can remove its old path.
	for _, previous := range old {
		before := previous.(map[string]any)
		for _, item := range merged {
			after := item.(map[string]any)
			if before["name"] == after["name"] && before["path"] != after["path"] {
				delete(contents, DraftString(before, "path"))
			}
		}
	}
	for _, item := range merged {
		obj := item.(map[string]any)
		path := DraftString(obj, "path")
		name := DraftString(obj, "name")
		fromIncoming := false
		for _, candidate := range edit.Items {
			v := candidate.(map[string]any)
			if DraftString(v, "name") == name {
				fromIncoming = true
			}
		}
		if edit.Conflict == "keep" {
			for _, candidate := range old {
				v := candidate.(map[string]any)
				if DraftString(v, "name") == name {
					fromIncoming = false
				}
			}
		}
		if fromIncoming {
			if content, ok := edit.Skills[path]; ok {
				contents[path] = content
			}
		} else if content, ok := d.Skills[path]; ok {
			contents[path] = content
		}
	}
	d.Manifest["skills"] = merged
	d.Skills = contents
	return nil
}
