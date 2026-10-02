package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	server := flag.String("server", fallback(os.Getenv("AGENSTRA_SERVER"), "http://127.0.0.1:8091"), "management server URL")
	keyEnv := flag.String("admin-key-env", "AGENSTRA_ADMIN_API_KEY", "admin key variable")
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		fail(errors.New("command required: model, draft, list, validate, publish, activate, bind, disable, check, audit"))
	}
	base, e := validateServer(*server)
	if e != nil {
		fail(e)
	}
	key := os.Getenv(*keyEnv)
	if key == "" {
		fail(fmt.Errorf("missing administrator key in %s", *keyEnv))
	}
	if args[0] == "model" {
		out, err := modelCommand(args[1:], func(method, path string, body any) (any, error) {
			return managementRequest(base, key, method, path, body)
		})
		if err != nil {
			fail(err)
		}
		printResult(out)
		return
	}
	if args[0] == "draft" {
		out, err := draftCommand(args[1:], func(method, path string, body any) (any, error) {
			return managementRequest(base, key, method, path, body)
		})
		if err != nil {
			fail(err)
		}
		printResult(out)
		return
	}
	method, path := "GET", "/admin/api/overview"
	var body any
	switch args[0] {
	case "list":
		if len(args) != 1 {
			fail(errors.New("list takes no arguments"))
		}
	case "audit":
		if len(args) != 1 {
			fail(errors.New("audit takes no arguments"))
		}
		path = "/admin/api/audit"
	case "validate", "publish":
		if len(args) < 2 {
			fail(errors.New("manifest path required"))
		}
		fs := flag.NewFlagSet(args[0], flag.ExitOnError)
		version := fs.String("version", "", "version for legacy pack")
		_ = fs.Parse(args[2:])
		body, e = packageBody(args[1], *version)
		if e != nil {
			fail(e)
		}
		method = "POST"
		if args[0] == "validate" {
			path = "/admin/api/validate"
		} else {
			path = "/admin/api/releases"
		}
	case "activate":
		if len(args) < 3 {
			fail(errors.New("activate PACK_ID DIGEST [--revision N]"))
		}
		fs := flag.NewFlagSet("activate", flag.ExitOnError)
		revision := fs.Int("revision", -1, "expected current revision")
		_ = fs.Parse(args[3:])
		var rev any
		if *revision >= 0 {
			rev = *revision
		}
		body = map[string]any{"digest": args[2], "expected_revision": rev}
		method = "POST"
		path = "/admin/api/packs/" + url.PathEscape(args[1]) + "/activate"
	case "bind":
		if len(args) != 4 {
			fail(errors.New("bind OWNER_ID PACK_ID CONFIG_JSON"))
		}
		b, e := os.ReadFile(args[3])
		if e != nil {
			fail(e)
		}
		if e = json.Unmarshal(b, &body); e != nil {
			fail(e)
		}
		method = "PUT"
		path = "/admin/api/bindings/" + url.PathEscape(args[1]) + "/" + url.PathEscape(args[2])
	case "disable", "check":
		if len(args) != 3 {
			fail(fmt.Errorf("%s OWNER_ID PACK_ID", args[0]))
		}
		path = "/admin/api/bindings/" + url.PathEscape(args[1]) + "/" + url.PathEscape(args[2])
		if args[0] == "disable" {
			method = "DELETE"
		} else {
			method = "POST"
			path += "/check"
		}
	default:
		fail(fmt.Errorf("unknown command: %s", args[0]))
	}
	out, e := managementRequest(base, key, method, path, body)
	if e != nil {
		fail(e)
	}
	printResult(out)
}
func managementRequest(base, key, method, path string, body any) (any, error) {
	var payload io.Reader
	if body != nil {
		b, e := json.Marshal(body)
		if e != nil {
			return nil, e
		}
		payload = bytes.NewReader(b)
	}
	req, e := http.NewRequest(method, base+path, payload)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, e := client.Do(req)
	if e != nil {
		return nil, e
	}
	defer resp.Body.Close()
	data, e := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if e != nil {
		return nil, e
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("management API returned %d: %s", resp.StatusCode, string(data))
	}
	var out any
	if e = json.Unmarshal(data, &out); e != nil {
		return nil, e
	}
	return out, nil
}
func printResult(out any) {
	pretty, e := json.MarshalIndent(out, "", "  ")
	if e != nil {
		fail(e)
	}
	fmt.Println(string(pretty))
}
func packageBody(path, version string) (map[string]any, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	var manifest map[string]any
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	if e = decoder.Decode(&manifest); e != nil {
		return nil, e
	}
	root, e := filepath.Abs(filepath.Dir(path))
	if e != nil {
		return nil, e
	}
	root, e = filepath.EvalSymlinks(root)
	if e != nil {
		return nil, e
	}
	skills := map[string]string{}
	if entries, ok := manifest["skills"].([]any); ok {
		for _, v := range entries {
			entry, ok := v.(map[string]any)
			if !ok {
				return nil, errors.New("invalid skill entry")
			}
			name, ok := entry["path"].(string)
			if !ok {
				return nil, errors.New("invalid skill path")
			}
			file := filepath.Join(root, filepath.FromSlash(name))
			clean, e := filepath.EvalSymlinks(file)
			if e != nil {
				return nil, e
			}
			rel, e := filepath.Rel(root, clean)
			if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
				return nil, fmt.Errorf("skill path must stay inside pack directory: %s", name)
			}
			content, e := os.ReadFile(clean)
			if e != nil {
				return nil, e
			}
			skills[name] = string(content)
		}
	}
	if version == "" {
		version, _ = manifest["version"].(string)
	}
	return map[string]any{"pack_id": manifest["name"], "version": version, "manifest": manifest, "skills": skills}, nil
}
func validateServer(raw string) (string, error) {
	u, e := url.Parse(raw)
	if e != nil {
		return "", e
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("management URL cannot contain credentials, a query, or a fragment")
	}
	if u.Scheme == "https" && u.Hostname() != "" {
		return strings.TrimRight(raw, "/"), nil
	}
	if u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1" || u.Hostname() == "::1") {
		return strings.TrimRight(raw, "/"), nil
	}
	return "", errors.New("management URL must use HTTPS, or HTTP on localhost")
}
func fallback(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
func fail(e error) { fmt.Fprintln(os.Stderr, "agenstra-manage:", e); os.Exit(2) }
