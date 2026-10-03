package agenstra

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServerAuthReadinessAndOwnerIsolation(t *testing.T) {
	dir := t.TempDir()
	d := &Deployment{BaseDir: dir, Config: DeploymentConfig{DatabasePath: "runs.sqlite3", Users: map[string]UserConfig{"alice": {APIKeyEnv: "ALICE_KEY"}, "bob": {APIKeyEnv: "BOB_KEY"}}}, Environment: map[string]string{"ALICE_KEY": "alice-secret", "BOB_KEY": "bob-secret"}}
	store, e := NewSQLiteStore(filepath.Join(dir, "runs.sqlite3"))
	if e != nil {
		t.Fatal(e)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(store.Close)
	host := NewAgentHost(store, nil, nil, nil)
	server, e := NewHTTPServer(host, d, false, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(server.Close)
	request := func(path, token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", path, nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, req)
		return w
	}
	if w := request("/readyz", ""); w.Code != 200 {
		t.Fatalf("readyz: %d", w.Code)
	}
	if w := request("/runs", ""); w.Code != 401 {
		t.Fatalf("auth: %d", w.Code)
	}
	if w := request("/runs", "alice-secret"); w.Code != 200 || string(w.Body.Bytes()) != "[]\n" {
		t.Fatalf("alice runs: %d %s", w.Code, w.Body.String())
	}
	if w := request("/runs", "bob-secret"); w.Code != 200 {
		t.Fatalf("bob runs: %d", w.Code)
	}
	if w := request("/runs/unknown", "bob-secret"); w.Code != 404 {
		t.Fatalf("owner scoped missing: %d %s", w.Code, w.Body.String())
	} else {
		var data map[string]any
		if e = json.Unmarshal(w.Body.Bytes(), &data); e != nil || data["code"] != "not_found" {
			t.Fatalf("error envelope: %v %v", data, e)
		}
	}
}

type slowServerModel struct{ started chan struct{} }

func (m *slowServerModel) Decide(ctx context.Context, _ ContextPacket, _ string) (Decision, error) {
	select {
	case m.started <- struct{}{}:
	default:
	}
	select {
	case <-time.After(150 * time.Millisecond):
		return Decision{Schema: "agenstra.decision.v1", Kind: "final", AnswerMarkdown: "Done", FactIDs: []string{}}, nil
	case <-ctx.Done():
		return Decision{}, ctx.Err()
	}
}
func TestWorkerDoesNotCancelAtPollInterval(t *testing.T) {
	dir := t.TempDir()
	d := &Deployment{BaseDir: dir, Config: DeploymentConfig{DatabasePath: "runs.sqlite3", Users: map[string]UserConfig{"alice": {APIKeyEnv: "ALICE_KEY"}}}, Environment: map[string]string{"ALICE_KEY": "alice-secret"}}
	store, e := NewSQLiteStore(filepath.Join(dir, "runs.sqlite3"))
	if e != nil {
		t.Fatal(e)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(store.Close)
	model := &slowServerModel{started: make(chan struct{}, 1)}
	provider := &hostProvider{}
	host := NewAgentHost(store, func(context.Context, string, string) (CapabilityProvider, error) { return provider, nil }, model, func(context.Context, string, string) (ExecutionPolicy, error) {
		return ExecutionPolicy{GrantedCapabilities: map[string]bool{}, AllowModelData: true}, nil
	})
	server, e := NewHTTPServer(host, d, true, 20*time.Millisecond)
	if e != nil {
		t.Fatal(e)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(server.Close)
	run, e := host.Create(t.Context(), "alice", "records", "Say done", "")
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-model.started:
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not start")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		current, e := store.GetRun(run.RunID, "alice")
		if e != nil {
			t.Fatal(e)
		}
		if current.Status == "completed" {
			return
		}
		if current.Status == "failed" {
			t.Fatalf("worker failed after polling interval: %v", current.State)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("worker did not finish")
}

type blockingServerModel struct{ started chan struct{} }

func (m *blockingServerModel) Decide(ctx context.Context, _ ContextPacket, _ string) (Decision, error) {
	select {
	case m.started <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return Decision{}, ctx.Err()
}
func TestWorkerShutdownCancelsInFlightModel(t *testing.T) {
	dir := t.TempDir()
	d := &Deployment{BaseDir: dir, Config: DeploymentConfig{DatabasePath: "runs.sqlite3", Users: map[string]UserConfig{"alice": {APIKeyEnv: "ALICE_KEY"}}}, Environment: map[string]string{"ALICE_KEY": "alice-secret"}}
	store, e := NewSQLiteStore(filepath.Join(dir, "runs.sqlite3"))
	if e != nil {
		t.Fatal(e)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(store.Close)
	model := &blockingServerModel{started: make(chan struct{}, 1)}
	host := NewAgentHost(store, func(context.Context, string, string) (CapabilityProvider, error) { return &hostProvider{}, nil }, model, func(context.Context, string, string) (ExecutionPolicy, error) {
		return ExecutionPolicy{GrantedCapabilities: map[string]bool{}, AllowModelData: true}, nil
	})
	server, e := NewHTTPServer(host, d, true, 20*time.Millisecond)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = host.Create(t.Context(), "alice", "records", "Say done", ""); e != nil {
		t.Fatal(e)
	}
	select {
	case <-model.started:
	case <-time.After(2 * time.Second):
		if err := server.Close(); err != nil {
			t.Error(err)
		}
		t.Fatal("worker did not start")
	}
	done := make(chan error, 1)
	go func() { done <- server.Close() }()
	select {
	case e = <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel model")
	}
}

func TestServerRequiredBodyFields(t *testing.T) {
	dir := t.TempDir()
	d := &Deployment{BaseDir: dir, Config: DeploymentConfig{DatabasePath: "runs.sqlite3", Users: map[string]UserConfig{"alice": {APIKeyEnv: "ALICE_KEY"}}}, Environment: map[string]string{"ALICE_KEY": "alice-secret"}}
	store, e := NewSQLiteStore(filepath.Join(dir, "runs.sqlite3"))
	if e != nil {
		t.Fatal(e)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(store.Close)
	host := NewAgentHost(store, nil, nil, nil)
	server, e := NewHTTPServer(host, d, false, time.Second)
	if e != nil {
		t.Fatal(e)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(server.Close)
	cases := []struct{ path, body string }{{"/runs", `{"pack_id":"records","instruction":"do it","request_id":""}`}, {"/runs/unknown/input", `{"field":"id","revision":0}`}, {"/runs/unknown/input", `{"field":"id","text":"x"}`}, {"/runs/unknown/approval", `{"invocation_id":"call-1","arguments_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`}}
	for _, tc := range cases {
		req := httptest.NewRequest("POST", tc.path, strings.NewReader(tc.body))
		req.Header.Set("Authorization", "Bearer alice-secret")
		w := httptest.NewRecorder()
		server.Handler().ServeHTTP(w, req)
		if w.Code != 422 {
			t.Errorf("%s body %s: got %d, want 422: %s", tc.path, tc.body, w.Code, w.Body.String())
		}
	}
}
