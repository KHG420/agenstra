package agenstra

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func memoryHTTP(t *testing.T, s *HTTPServer, method, path, token, origin, body string, want int) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	if w.Code != want {
		t.Fatalf("%s %s: %d want %d: %s", method, path, w.Code, want, w.Body.String())
	}
	return w
}

func TestMemoryHTTPManagementAuthenticationHistoryAndRevisionChecks(t *testing.T) {
	s, h := scheduleServer(t, false)
	path := "/memories?pack_id=records"
	memoryHTTP(t, s, "GET", path, "", "", "", 401)
	w := memoryHTTP(t, s, "POST", path, "alice-secret", "", `{"scope":"pack","key":"report.language","value":"zh-CN","kind":"preference","revision":0}`, 200)
	var m Memory
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil || m.ID == "" || m.Origin != "manual" || m.Revision != 1 {
		t.Fatal(m, err)
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("memory response was cacheable")
	}
	item := "/memories/" + m.ID + "?pack_id=records"
	memoryHTTP(t, s, "GET", item, "alice-secret", "", "", 200)
	memoryHTTP(t, s, "GET", item, "bob-secret", "", "", 404)
	memoryHTTP(t, s, "GET", "/memories/"+m.ID+"?pack_id=other", "alice-secret", "", "", 404)
	memoryHTTP(t, s, "GET", "/memories/"+m.ID+"/history?pack_id=records", "bob-secret", "", "", 404)
	for _, body := range []string{
		`{"scope":"pack","key":"report.language","value":"en","kind":"preference","revision":0}`,
		`{"scope":"pack","key":"report.language","value":"en","kind":"preference","revision":99}`,
	} {
		memoryHTTP(t, s, "POST", path, "alice-secret", "", body, 409)
	}
	memoryHTTP(t, s, "POST", path, "alice-secret", "", `{"scope":"pack","key":"report.language","value":"en","kind":"preference","revision":1}`, 200)
	w = memoryHTTP(t, s, "GET", "/memories/"+m.ID+"/history?pack_id=records", "alice-secret", "", "", 200)
	var history MemoryHistory
	if err := json.Unmarshal(w.Body.Bytes(), &history); err != nil || len(history.Revisions) != 2 || history.Revisions[0].Value != "en" {
		t.Fatal(history, err)
	}
	memoryHTTP(t, s, "DELETE", item, "bob-secret", "", `{"revision":2}`, 404)
	memoryHTTP(t, s, "DELETE", item, "alice-secret", "", `{"revision":1}`, 409)
	w = memoryHTTP(t, s, "DELETE", item, "alice-secret", "", `{"revision":2}`, 200)
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil || m.Status != "forgotten" || m.Value != "" {
		t.Fatal(m, err)
	}
	for _, body := range []string{
		`{"scope":"user","key":"report.format","value":"table","kind":"preference"}`,
		`{"scope":"user","key":"report.format","value":"table","kind":"preference","revision":0,"owner_id":"bob"}`,
		`{"scope":"global","key":"report.format","value":"table","kind":"preference","revision":0}`,
		`{"scope":"user","key":"report.format","value":"table","kind":"preference","revision":0,"pack_id":"other"}`,
	} {
		memoryHTTP(t, s, "POST", path, "alice-secret", "", body, 422)
	}
	memoryHTTP(t, s, "GET", path+"&limit=0", "alice-secret", "", "", 422)
	history, err := h.MemoryHistory(t.Context(), "alice", "records", m.ID)
	if err != nil || len(history.Revisions) != 1 || history.Revisions[0].Value != "" {
		t.Fatal("forgotten history retained content", history, err)
	}
}

func TestMemoryWebTicketsResolveOwnerAndPackWithoutBrowserSession(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	f.w.Close()
	f.d.Config.Users["bob"] = UserConfig{APIKeyEnv: "BOB_KEY"}
	f.d.Environment["BOB_KEY"] = "bob-key"
	f.d.Config.WebIntegration.BrowserBridge = false
	delete(f.d.Config.WebIntegration.Integrations, "records-web")
	h := testHost(t, f.h.Store, f.p, &hostModel{})
	s, err := NewHTTPServer(h, f.d, false, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	mint := func(key string) string {
		w := memoryHTTP(t, s, "POST", "/web/v1/token", key, "", "", 200)
		var b struct{ Token string }
		if err := json.Unmarshal(w.Body.Bytes(), &b); err != nil || b.Token == "" {
			t.Fatal(b, err)
		}
		return b.Token
	}
	alice, bob := mint("alice-key"), mint("bob-key")
	base := "/web/v1/memories?integration_id=records"
	body := `{"scope":"user","key":"response.detail","value":"concise","kind":"preference","revision":0}`
	memoryHTTP(t, s, "POST", base, "alice-key", "", body, 401)
	w := memoryHTTP(t, s, "POST", base, alice, "", body, 200)
	var m Memory
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil || m.PackID != "" {
		t.Fatal(m, err)
	}
	item := "/web/v1/memories/" + m.ID + "?integration_id=records"
	memoryHTTP(t, s, "GET", item, bob, "", "", 404)
	memoryHTTP(t, s, "GET", item, alice, "https://other.example", "", 403)
	memoryHTTP(t, s, "GET", "/memories?pack_id=records", alice, "", "", 401)
	memoryHTTP(t, s, "GET", base+"&pack_id=other", alice, "", "", 422)
	memoryHTTP(t, s, "POST", base, alice, "", `{"scope":"pack","key":"response.detail","value":"concise","kind":"preference","revision":0,"pack_id":"other"}`, 422)
	w = memoryHTTP(t, s, "GET", base, alice, "", "", 200)
	var items []Memory
	if err := json.Unmarshal(w.Body.Bytes(), &items); err != nil || len(items) != 1 || items[0].ID != m.ID {
		t.Fatal(items, err)
	}
	memoryHTTP(t, s, "GET", "/web/v1/memories/"+m.ID+"/history?integration_id=records", alice, "", "", 200)
	memoryHTTP(t, s, "DELETE", item, alice, "", fmt.Sprintf(`{"revision":%d}`, m.Revision), 200)
	memoryHTTP(t, s, "DELETE", item, alice, "", fmt.Sprintf(`{"revision":%d}`, m.Revision), 409)
}
