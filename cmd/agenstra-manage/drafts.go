package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type managementCall func(method, path string, body any) (any, error)
type stringList []string

func (v *stringList) String() string     { return strings.Join(*v, ",") }
func (v *stringList) Set(s string) error { *v = append(*v, s); return nil }
func readJSON(path string, dst any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	if err = dec.Decode(dst); err != nil {
		return err
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return errors.New("trailing JSON data")
	}
	return nil
}
func draftCommand(args []string, call managementCall) (any, error) {
	usage := errors.New("draft commands: list; create ID [--type rest|mcp --name NAME --version VERSION]; show ID; set ID basic|connection FILE; add ID FILE [--conflict error|keep|replace]; update ID NAME FILE; import ID PACK_JSON [--conflict ...]; openapi ID SPEC_JSON --operation ID [--operation ID --conflict ...]; discover MCP_DRAFT_ID [ENVIRONMENT_REFS_JSON]; skill ID NAME FILE [--path PATH --description TEXT --conflict ...]; remove ID capability|skill NAME; edit ID; export ID NEW_DIRECTORY; validate ID; publish ID")
	if len(args) == 0 {
		return nil, usage
	}
	command := args[0]
	if command == "list" {
		if len(args) != 1 {
			return nil, usage
		}
		return call("GET", "/admin/api/drafts", nil)
	}
	if len(args) < 2 {
		return nil, usage
	}
	id := args[1]
	path := "/admin/api/drafts/" + url.PathEscape(id)
	if command == "create" {
		fs := flag.NewFlagSet("draft create", flag.ContinueOnError)
		typ := fs.String("type", "rest", "rest or mcp")
		name := fs.String("name", id, "pack name")
		version := fs.String("version", "1.0.0", "pack version")
		if err := fs.Parse(args[2:]); err != nil {
			return nil, err
		}
		if fs.NArg() != 0 {
			return nil, usage
		}
		schema := "agenstra.rest-pack.v2"
		key := "capabilities"
		if *typ == "mcp" {
			schema = "agenstra.mcp-pack.v1"
			key = "tools"
		} else if *typ != "rest" {
			return nil, errors.New("type must be rest or mcp")
		}
		return call("PUT", path, map[string]any{"expected_revision": 0, "manifest": map[string]any{"schema": schema, "name": *name, "version": *version, "guidance": "", key: []any{}}, "skills": map[string]string{}})
	}
	result, err := call("GET", path, nil)
	if err != nil {
		return nil, err
	}
	d, ok := result.(map[string]any)
	if !ok {
		return nil, errors.New("invalid draft response")
	}
	m, ok := d["manifest"].(map[string]any)
	if !ok {
		return nil, errors.New("invalid draft manifest")
	}
	body := map[string]any{"expected_revision": d["revision"]}
	conflict := "error"
	switch command {
	case "discover":
		if len(args) < 2 || len(args) > 3 || m["schema"] != "agenstra.mcp-pack.v1" {
			return nil, errors.New("draft discover MCP_DRAFT_ID [ENVIRONMENT_REFS_JSON]")
		}
		if len(args) == 3 {
			var environment map[string]string
			if err := readJSON(args[2], &environment); err != nil {
				return nil, err
			}
			body["environment"] = environment
		}
		return call("POST", path+"/discover", body)
	case "show":
		if len(args) != 2 {
			return nil, usage
		}
		return d, nil
	case "validate", "publish":
		if len(args) != 2 {
			return nil, usage
		}
		return call("POST", path+"/"+command, body)
	case "set":
		if len(args) != 4 || (args[2] != "basic" && args[2] != "connection") {
			return nil, usage
		}
		var value map[string]any
		if err = readJSON(args[3], &value); err != nil {
			return nil, err
		}
		body["section"] = args[2]
		body["value"] = value
	case "add", "import", "openapi":
		if len(args) < 3 {
			return nil, usage
		}
		fs := flag.NewFlagSet("draft "+command, flag.ContinueOnError)
		fs.StringVar(&conflict, "conflict", "error", "error, keep, or replace existing names")
		var operations stringList
		fs.Var(&operations, "operation", "selected OpenAPI operationId (repeatable)")
		if err = fs.Parse(args[3:]); err != nil {
			return nil, err
		}
		if fs.NArg() != 0 {
			return nil, usage
		}
		body["conflict"] = conflict
		if command == "import" {
			pack, err := packageBody(args[2], "")
			if err != nil {
				return nil, err
			}
			body["section"] = "import"
			body["value"] = pack["manifest"]
			body["skills"] = pack["skills"]
		} else if command == "openapi" {
			var spec map[string]any
			if err = readJSON(args[2], &spec); err != nil {
				return nil, err
			}
			if len(operations) == 0 {
				return nil, errors.New("select at least one --operation")
			}
			body["section"] = "openapi"
			body["value"] = map[string]any{"spec": spec, "operations": operations}
		} else {
			var value any
			if err = readJSON(args[2], &value); err != nil {
				return nil, err
			}
			items, ok := value.([]any)
			if !ok {
				if _, ok = value.(map[string]any); !ok {
					return nil, errors.New("capability file must contain an object or array")
				}
				items = []any{value}
			}
			body["section"] = "capabilities"
			body["items"] = items
		}
	case "update":
		if len(args) != 4 {
			return nil, usage
		}
		var value map[string]any
		if err = readJSON(args[3], &value); err != nil {
			return nil, err
		}
		key := "capabilities"
		if m["schema"] == "agenstra.mcp-pack.v1" {
			key = "tools"
		}
		items, _ := m[key].([]any)
		var item map[string]any
		for _, v := range items {
			obj, _ := v.(map[string]any)
			if obj["name"] == args[2] {
				item = obj
				break
			}
		}
		if item == nil {
			return nil, errors.New("capability not found; use draft add to create it")
		}
		for k, v := range value {
			if k == "name" && v != args[2] {
				return nil, errors.New("update cannot rename a capability")
			}
			if v == nil {
				delete(item, k)
			} else {
				item[k] = v
			}
		}
		body["section"] = "capabilities"
		body["items"] = []any{item}
		body["conflict"] = "replace"
	case "skill":
		if len(args) < 4 {
			return nil, usage
		}
		fs := flag.NewFlagSet("draft skill", flag.ContinueOnError)
		p := fs.String("path", "skills/"+args[2]+"/SKILL.md", "relative path inside the pack")
		description := fs.String("description", args[2], "skill description")
		fs.StringVar(&conflict, "conflict", "error", "error, keep, or replace")
		if err = fs.Parse(args[4:]); err != nil {
			return nil, err
		}
		if fs.NArg() != 0 {
			return nil, usage
		}
		content, err := os.ReadFile(args[3])
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(content)
		body["section"] = "skills"
		body["conflict"] = conflict
		body["items"] = []any{map[string]any{"name": args[2], "description": *description, "path": *p, "sha256": hex.EncodeToString(sum[:])}}
		body["skills"] = map[string]string{*p: string(content)}
	case "remove":
		if len(args) != 4 || (args[2] != "capability" && args[2] != "skill") {
			return nil, usage
		}
		body["section"] = "remove_" + args[2]
		body["name"] = args[3]
	case "edit":
		if len(args) != 2 {
			return nil, usage
		}
		editor := strings.Fields(os.Getenv("EDITOR"))
		if len(editor) == 0 {
			return nil, errors.New("set EDITOR to edit the manifest")
		}
		f, err := os.CreateTemp("", "agenstra-draft-*.json")
		if err != nil {
			return nil, err
		}
		keepFile := false
		defer func() {
			if !keepFile {
				os.Remove(f.Name())
			}
		}()
		data, err := json.MarshalIndent(m, "", "  ")
		if err != nil {
			f.Close()
			return nil, err
		}
		_, err = f.Write(data)
		closeErr := f.Close()
		if err != nil {
			return nil, err
		}
		if closeErr != nil {
			return nil, closeErr
		}
		cmd := exec.Command(editor[0], append(editor[1:], f.Name())...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err = cmd.Run(); err != nil {
			return nil, err
		}
		if err = readJSON(f.Name(), &m); err != nil {
			keepFile = true
			return nil, fmt.Errorf("%w; edited manifest retained at %s", err, f.Name())
		}
		body["manifest"] = m
		body["skills"] = d["skills"]
		result, err := call("PUT", path, body)
		if err != nil {
			keepFile = true
			return nil, fmt.Errorf("%w; edited manifest retained at %s", err, f.Name())
		}
		return result, nil
	case "export":
		if len(args) != 3 {
			return nil, usage
		}
		// A new directory avoids following existing symlinks or overwriting other files.
		if err = os.Mkdir(args[2], 0755); err != nil {
			return nil, err
		}
		files := map[string]string{}
		raw, _ := json.MarshalIndent(m, "", "  ")
		files["pack.json"] = string(raw) + "\n"
		skills, _ := d["skills"].(map[string]any)
		for p, v := range skills {
			if p == "pack.json" || strings.Contains(p, "\\") || strings.HasPrefix(p, "/") {
				return nil, errors.New("invalid skill path")
			}
			for _, part := range strings.Split(p, "/") {
				if part == "" || part == "." || part == ".." {
					return nil, errors.New("invalid skill path")
				}
			}
			content, ok := v.(string)
			if !ok {
				return nil, errors.New("invalid skill content")
			}
			files[p] = content
		}
		for p, content := range files {
			target := filepath.Join(args[2], filepath.FromSlash(p))
			if err = os.MkdirAll(filepath.Dir(target), 0755); err != nil {
				return nil, err
			}
			if err = os.WriteFile(target, []byte(content), 0644); err != nil {
				return nil, err
			}
		}
		return map[string]any{"directory": args[2], "draft_id": id, "revision": d["revision"]}, nil
	default:
		return nil, fmt.Errorf("unknown draft command %q: %w", command, usage)
	}
	return call("PATCH", path, body)
}
