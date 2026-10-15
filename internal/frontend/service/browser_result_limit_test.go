package service

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func resultLimitProfile(t *testing.T, limit int) agentcontract.FrontendProfile {
	t.Helper()
	raw, err := json.Marshal(frontendTestProfile(false))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	document["actions"].([]any)[0].(map[string]any)["max_result_bytes"] = limit
	raw, err = json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var profile agentcontract.FrontendProfile
	if err := json.Unmarshal(raw, &profile); err != nil {
		t.Fatal(err)
	}
	return profile
}

func newResultLimitFixture(t *testing.T, model *hostModel, limit int) *webFixture {
	t.Helper()
	f := newWebFixture(t, model, false)
	profile, err := compileFrontend(resultLimitProfile(t, limit))
	if err != nil {
		t.Fatal(err)
	}
	f.w.profiles["records-web"] = profile
	f.session, f.key, err = f.w.CreateBrowserSession(t.Context(), "alice", "records-web", "1", []string{"ui.navigate"})
	if err != nil {
		t.Fatal(err)
	}
	f.session, err = f.w.UpdatePageObservation("alice", f.session.ID, f.key, 1, 0, agentcontract.JSON{"page": "home"})
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestFrontendResultLimitDeclarationAndDefaultDigest(t *testing.T) {
	legacy := frontendTestProfile(false)
	compiled, err := compileFrontend(legacy)
	if err != nil || compiled.digest != agentcontract.WebHash(legacy) {
		t.Fatal("default profile identity changed", err)
	}
	raw, err := json.Marshal(compiled.profile)
	if err != nil || bytes.Contains(raw, []byte("max_result_bytes")) {
		t.Fatal("omitted default became a persisted profile field", err)
	}
	for _, limit := range []int{-1, (64 << 20) + 1} {
		if _, err := compileFrontend(resultLimitProfile(t, limit)); err == nil {
			t.Errorf("invalid result limit %d accepted", limit)
		}
	}
	for _, limit := range []int{0, 1, 1 << 20, 4 << 20, 64 << 20} {
		if _, err := compileFrontend(resultLimitProfile(t, limit)); err != nil {
			t.Errorf("valid result limit %d rejected: %v", limit, err)
		}
	}
}

func TestBrowserDeclaredResultLimitExactBoundary(t *testing.T) {
	const limit = 2 << 20
	for _, status := range []string{"succeeded", "failed", "unknown"} {
		for _, delta := range []int{0, 1} {
			t.Run(status+"/"+strconv.Itoa(delta), func(t *testing.T) {
				f := newResultLimitFixture(t, &hostModel{decisions: browserDecisions()}, limit)
				f.run(t)
				command := f.dispatch(t)
				if accepted, _, err := f.w.BeginBrowserCommand(t.Context(), "alice", command.ID, f.key, 1); err != nil || !accepted {
					t.Fatal(accepted, err)
				}
				empty, err := agentcontract.CanonicalJSON(agentcontract.JSON{"page": ""})
				if err != nil {
					t.Fatal(err)
				}
				result := agentcontract.JSON{"page": strings.Repeat("x", limit+delta-len(empty))}
				code := ""
				if status != "succeeded" {
					code = "host_action_rejected"
				}
				for range 2 {
					saved, err := f.w.CompleteBrowserCommand("alice", command.ID, f.key, 1, status, result, code)
					if delta == 0 {
						if err != nil || saved.Status != status || agentcontract.WebHash(saved.Result) != agentcontract.WebHash(result) {
							t.Fatal("declared result did not retain exact receipt", saved.Status, err)
						}
					} else if agentcontract.ErrorCode(err) != "browser_result_invalid" {
						t.Fatal("over-limit result was accepted", err)
					}
				}
				if delta != 0 {
					saved, err := f.w.command("alice", command.ID)
					if err != nil || saved.Status != "running" || saved.Result != nil || saved.ErrorCode != "" {
						t.Fatal("rejected result changed command", saved.Status, err)
					}
				}
			})
		}
	}
}

type observedResultBody struct {
	io.Reader
	reads int
}

func (b *observedResultBody) Read(p []byte) (int, error) {
	b.reads++
	return b.Reader.Read(p)
}

func (b *observedResultBody) Close() error { return nil }

func TestBrowserLargeResultHTTPAuthenticatesBeforeReadingAndRetainsCompleteBody(t *testing.T) {
	f := newResultLimitFixture(t, &hostModel{decisions: browserDecisions()}, 4<<20)
	f.run(t)
	command := f.dispatch(t)
	if accepted, _, err := f.w.BeginBrowserCommand(t.Context(), "alice", command.ID, f.key, 1); err != nil || !accepted {
		t.Fatal(accepted, err)
	}
	page := strings.Repeat("x", 3<<20)
	raw, err := json.Marshal(agentcontract.JSON{"generation": 1, "status": "succeeded", "result": agentcontract.JSON{"page": page}, "error_code": ""})
	if err != nil {
		t.Fatal(err)
	}
	server := &HTTPServer{Web: f.w}
	for _, identity := range []struct{ owner, key string }{{"other", f.key}, {"alice", "wrong-key"}, {"alice", ""}} {
		body := &observedResultBody{Reader: bytes.NewReader(raw)}
		request := httptest.NewRequest("POST", "/browser/v1/commands/"+command.ID+"/result", body)
		request.Header.Set("X-Agenstra-Browser-Key", identity.key)
		response := httptest.NewRecorder()
		server.browserHTTP(response, request, identity.owner)
		if response.Code < 400 || body.reads != 0 {
			t.Errorf("unauthorized large body read: code=%d reads=%d", response.Code, body.reads)
		}
	}
	oversized, err := json.Marshal(agentcontract.JSON{"generation": 1, "status": "succeeded", "result": agentcontract.JSON{"page": strings.Repeat("x", 4<<20)}, "error_code": ""})
	if err != nil {
		t.Fatal(err)
	}
	for _, invalid := range [][]byte{
		bytes.Replace(raw, []byte(`"generation":1`), []byte(`"generation":2`), 1),
		oversized,
		append(append([]byte{}, raw...), []byte(" {}")...),
		append(append([]byte{}, raw...), bytes.Repeat([]byte(" "), 2<<20)...),
		bytes.Replace(raw, []byte(`"status":`), []byte(`"unexpected":true,"status":`), 1),
	} {
		request := httptest.NewRequest("POST", "/browser/v1/commands/"+command.ID+"/result", bytes.NewReader(invalid))
		request.Header.Set("X-Agenstra-Browser-Key", f.key)
		response := httptest.NewRecorder()
		server.browserHTTP(response, request, "alice")
		saved, err := f.w.command("alice", command.ID)
		if response.Code < 400 || err != nil || saved.Status != "running" || saved.Result != nil {
			t.Fatalf("invalid HTTP result changed command: code=%d status=%s err=%v", response.Code, saved.Status, err)
		}
	}
	request := httptest.NewRequest("POST", "/browser/v1/commands/"+command.ID+"/result", bytes.NewReader(raw))
	request.Header.Set("X-Agenstra-Browser-Key", f.key)
	response := httptest.NewRecorder()
	server.browserHTTP(response, request, "alice")
	if response.Code != 200 {
		t.Fatalf("declared HTTP result rejected: code=%d", response.Code)
	}
	saved, err := f.w.command("alice", command.ID)
	if err != nil || saved.Status != "succeeded" || saved.Result["page"] != page {
		t.Fatal("HTTP result lost complete evidence", saved.Status, err)
	}
	var body map[string]any
	if err := decodeBody(httptest.NewRequest("POST", "/other", bytes.NewReader(raw)), &body); err == nil {
		t.Fatal("ordinary HTTP body limit was expanded")
	}
}

func TestBrowserDeclaredResultLimitRemainsPinnedAfterProfileChange(t *testing.T) {
	f := newResultLimitFixture(t, &hostModel{decisions: browserDecisions()}, 2<<20)
	f.run(t)
	command := f.dispatch(t)
	if accepted, _, err := f.w.BeginBrowserCommand(t.Context(), "alice", command.ID, f.key, 1); err != nil || !accepted {
		t.Fatal(accepted, err)
	}
	replacement := resultLimitProfile(t, 1024)
	replacement.Version, replacement.HandlerVersion = "2", "2"
	compiled, err := compileFrontend(replacement)
	if err != nil {
		t.Fatal(err)
	}
	f.w.profiles["records-web"] = compiled
	result := agentcontract.JSON{"page": strings.Repeat("x", (2<<20)-32)}
	if _, err := f.w.CompleteBrowserCommand("alice", command.ID, f.key, 1, "unknown", result, "browser_connection_lost"); err != nil {
		t.Fatal("original pinned limit was replaced", err)
	}
	if _, err := f.w.CompleteBrowserCommand("alice", command.ID, f.key, 1, "succeeded", result, ""); err != nil {
		t.Fatal("original unknown result could not settle", err)
	}
	if accepted, _, err := f.w.BeginBrowserCommand(t.Context(), "alice", command.ID, f.key, 1); err != nil || accepted {
		t.Fatal("settled receipt authorized replay", accepted, err)
	}
}

func TestBrowserDeclaredLargeResultKeepsFullFactAndBoundedModelView(t *testing.T) {
	page := strings.Repeat("x", 3<<20)
	previewed := false
	model := &hostModel{decisions: browserDecisions(), hook: func(packet agentcontract.ContextPacket) {
		for _, fact := range packet.Facts {
			if fact.SourceCapability != "ui.command_status" {
				continue
			}
			raw, err := agentcontract.CanonicalJSON(fact)
			if err != nil || len(raw) >= 65536 || bytes.Contains(raw, []byte(page)) {
				t.Error("large result escaped bounded model preview", len(raw), err)
			}
			previewed = true
		}
	}}
	f := newResultLimitFixture(t, model, 4<<20)
	run := f.run(t)
	command := f.dispatch(t)
	if accepted, _, err := f.w.BeginBrowserCommand(t.Context(), "alice", command.ID, f.key, 1); err != nil || !accepted {
		t.Fatal(accepted, err)
	}
	if _, err := f.w.CompleteBrowserCommand("alice", command.ID, f.key, 1, "succeeded", agentcontract.JSON{"page": page}, ""); err != nil {
		t.Fatal(err)
	}
	f.now += 2
	run, err := f.h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "completed" || !previewed {
		t.Fatal("large result could not complete", run.Status, previewed, err)
	}
	raw, err := json.Marshal(run.State["artifact_ids"])
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	if err := json.Unmarshal(raw, &ids); err != nil {
		t.Fatal(err)
	}
	retained := false
	for _, id := range ids {
		artifact, err := f.h.Store.GetArtifact(run.RunID, id, "alice")
		if err != nil {
			t.Fatal(err)
		}
		raw, err := agentcontract.CanonicalJSON(artifact)
		if err != nil {
			t.Fatal(err)
		}
		retained = retained || bytes.Contains(raw, []byte(page))
	}
	if !retained {
		t.Fatal("full persisted result was replaced by its preview")
	}
}
