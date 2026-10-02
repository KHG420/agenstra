package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agenstra "github.com/KHG420/agenstra"
)

func TestEvaluationCreationRetriesTheSameIdentity(t *testing.T) {
	for _, first := range []string{"broken_json", "missing_id", "server_error", "lost_connection", "rejected"} {
		t.Run(first, func(t *testing.T) {
			id := agenstra.NewID()
			var creates atomic.Int32
			var requestID string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/runs" {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					current, _ := body["request_id"].(string)
					if current == "" || (requestID != "" && current != requestID) {
						t.Error("retry changed request identity")
					}
					requestID = current
					if creates.Add(1) == 1 {
						switch first {
						case "broken_json":
							_, _ = w.Write([]byte(`{"run_id":`))
						case "missing_id":
							_, _ = w.Write([]byte(`{}`))
						case "server_error":
							w.WriteHeader(http.StatusBadGateway)
						case "lost_connection":
							connection, _, err := w.(http.Hijacker).Hijack()
							if err != nil {
								t.Error(err)
								return
							}
							_ = connection.Close()
						case "rejected":
							w.WriteHeader(http.StatusForbidden)
						}
						return
					}
				}
				if strings.HasSuffix(r.URL.Path, "/diagnostics") {
					_ = json.NewEncoder(w).Encode(agenstra.RunDiagnostics{RunID: id, Status: "completed"})
					return
				}
				_ = json.NewEncoder(w).Encode(agenstra.StoredRun{RunID: id, PackID: "orders", Status: "completed", State: agenstra.JSON{"runtime": agenstra.RuntimeState{RunID: id, Status: "completed"}}})
			}))
			defer server.Close()
			client := evaluationClient{server.URL, "local-key", server.Client()}
			result := client.evaluate(t.Context(), agenstra.EvaluationCase{Name: "creation", PackID: "orders", Instruction: "Query"}, time.Second)
			if first == "rejected" {
				if result.ErrorCode != "evaluation_http_403" || creates.Load() != 1 {
					t.Fatal(result, creates.Load())
				}
				return
			}
			if !result.Passed || result.RequestID != requestID || creates.Load() != 2 {
				t.Fatal(result, creates.Load())
			}
		})
	}
}

func TestEvaluationUnconfirmedCreationReportsIdentityWithoutCancelling(t *testing.T) {
	var creates, cancels atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/cancel") {
			cancels.Add(1)
		}
		creates.Add(1)
		_, _ = w.Write([]byte(`{"run_id":`))
	}))
	defer server.Close()
	client := evaluationClient{server.URL, "local-key", server.Client()}
	result := client.evaluate(context.Background(), agenstra.EvaluationCase{Name: "creation", PackID: "orders", Instruction: "Query", RequestID: "acceptance-request"}, time.Second)
	if result.ErrorCode != "evaluation_create_outcome_unknown" || result.RequestID != "acceptance-request" || result.RunID != "" || creates.Load() != 2 || cancels.Load() != 0 {
		t.Fatal(result, creates.Load(), cancels.Load())
	}
}

func TestEvaluationHTTPAssertionsAndAttention(t *testing.T) {
	for _, status := range []string{"completed", "needs_approval"} {
		t.Run(status, func(t *testing.T) {
			id, factID := agenstra.NewID(), agenstra.NewID()
			var creates, approvals atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer local-user-key" {
					t.Error("user authentication missing")
				}
				if strings.HasSuffix(r.URL.Path, "/approval") {
					approvals.Add(1)
				}
				if r.URL.Path == "/runs" {
					creates.Add(1)
				}
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/diagnostics") {
					_ = json.NewEncoder(w).Encode(agenstra.RunDiagnostics{RunID: id, Status: status})
					return
				}
				_ = json.NewEncoder(w).Encode(agenstra.StoredRun{RunID: id, PackID: "orders", Status: status, State: agenstra.JSON{"runtime": agenstra.RuntimeState{RunID: id, Status: status, Facts: []agenstra.Fact{{FactID: factID, SourceCapability: "orders.get", Value: agenstra.JSON{"id": json.Number("9007199254740993")}}}, Observations: []agenstra.Observation{{Capability: "orders.get", Status: "succeeded"}}, Decisions: []agenstra.JSON{{"kind": "final", "fact_ids": []string{factID}}}}}})
			}))
			defer server.Close()
			t.Setenv("EVALUATION_TEST_KEY", "local-user-key")
			dir := t.TempDir()
			path := filepath.Join(dir, "cases.json")
			cases := `[{"name":"order query","pack_id":"orders","instruction":"Query order","required_capabilities":["orders.get"],"facts":[{"capability":"orders.get","path":["id"],"value":9007199254740993}]}]`
			if status == "needs_approval" {
				cases = `[{"name":"confirm first","pack_id":"orders","instruction":"Submit","expected_status":"needs_approval"}]`
			}
			if err := os.WriteFile(path, []byte(cases), 0600); err != nil {
				t.Fatal(err)
			}
			var output bytes.Buffer
			passed, err := run([]string{"--server", server.URL, "--key-env", "EVALUATION_TEST_KEY", "--cases", path}, &output)
			if err != nil || !passed || creates.Load() != 1 || approvals.Load() != 0 || strings.Contains(output.String(), "local-user-key") {
				t.Fatal(passed, err, output.String(), creates.Load(), approvals.Load())
			}
		})
	}
}

func TestEvaluationValidatesAllCasesBeforeCreatingAndCancelsOnlyOwnedTimeouts(t *testing.T) {
	id := agenstra.NewID()
	var creates, cancels atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/runs" {
			creates.Add(1)
		}
		if strings.HasSuffix(r.URL.Path, "/cancel") {
			cancels.Add(1)
		}
		_ = json.NewEncoder(w).Encode(agenstra.StoredRun{RunID: id, Status: "running"})
	}))
	defer server.Close()
	t.Setenv("EVALUATION_TEST_KEY", "local-user-key")
	path := filepath.Join(t.TempDir(), "cases.json")
	if err := os.WriteFile(path, []byte(`[{"name":"valid","pack_id":"orders","instruction":"Query"},{"name":"invalid","pack_id":"orders"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--server", server.URL, "--key-env", "EVALUATION_TEST_KEY", "--cases", path, "--timeout", "30ms"}
	var output bytes.Buffer
	if _, err := run(args, &output); err == nil || creates.Load() != 0 {
		t.Fatal("invalid cases caused execution", err)
	}
	for _, existing := range []bool{false, true} {
		c := agenstra.EvaluationCase{Name: "timeout", PackID: "orders", Instruction: "Query"}
		if existing {
			c.RunID = id
		}
		raw, _ := json.Marshal([]agenstra.EvaluationCase{c})
		_ = os.WriteFile(path, raw, 0600)
		output.Reset()
		passed, err := run(args, &output)
		if err != nil || passed {
			t.Fatal(passed, err, output.String())
		}
		if cancels.Load() != 1 {
			t.Fatal("existing task was cancelled or new task was not", cancels.Load())
		}
	}
}
