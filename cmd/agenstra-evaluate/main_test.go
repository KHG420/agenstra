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
	"sync"
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
							if _, callErr := w.Write([]byte(`{"run_id":`)); callErr != nil {
								t.Error(callErr)
							}
						case "missing_id":
							if _, callErr2 := w.Write([]byte(`{}`)); callErr2 != nil {
								t.Error(callErr2)
							}
						case "server_error":
							w.WriteHeader(http.StatusBadGateway)
						case "lost_connection":
							connection, _, err := w.(http.Hijacker).Hijack()
							if err != nil {
								t.Error(err)
								return
							}
							if callErr3 := connection.Close(); callErr3 != nil {
								t.Error(callErr3)
							}
						case "rejected":
							w.WriteHeader(http.StatusForbidden)
						}
						return
					}
				}
				if strings.HasSuffix(r.URL.Path, "/diagnostics") {
					if callErr4 := json.NewEncoder(w).Encode(agenstra.RunDiagnostics{RunID: id, Status: "completed"}); callErr4 != nil {
						t.Error(callErr4)
					}
					return
				}
				if callErr5 := json.NewEncoder(w).Encode(agenstra.StoredRun{RunID: id, PackID: "orders", Status: "completed", State: agenstra.JSON{"runtime": agenstra.RuntimeState{RunID: id, Status: "completed"}}}); callErr5 != nil {
					t.Error(callErr5)
				}
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

func TestEvaluationRepeatUsesFreshRunsAndReportsFailures(t *testing.T) {
	var mu sync.Mutex
	var ids, requests []string
	statuses := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.URL.Path == "/runs" {
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			request, _ := body["request_id"].(string)
			requests = append(requests, request)
			id := agenstra.NewID()
			ids = append(ids, id)
			status := "completed"
			if len(ids) == 2 {
				status = "failed"
			}
			statuses[id] = status
			if callErr6 := json.NewEncoder(w).Encode(agenstra.StoredRun{RunID: id, PackID: "orders", Status: status}); callErr6 != nil {
				t.Error(callErr6)
			}
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 2 {
			t.Error("unexpected path", r.URL.Path)
			return
		}
		id := parts[1]
		status := statuses[id]
		if len(parts) == 3 && parts[2] == "diagnostics" {
			if callErr7 := json.NewEncoder(w).Encode(agenstra.RunDiagnostics{RunID: id, Status: status, Findings: []agenstra.DiagnosticFinding{{Code: "business_failed"}}}); callErr7 != nil {
				t.Error(callErr7)
			}
			return
		}
		if callErr8 := json.NewEncoder(w).Encode(agenstra.StoredRun{RunID: id, PackID: "orders", Status: status, State: agenstra.JSON{"runtime": agenstra.RuntimeState{RunID: id, Status: status}}}); callErr8 != nil {
			t.Error(callErr8)
		}
	}))
	defer server.Close()
	t.Setenv("EVALUATION_TEST_KEY", "local-key")
	path := filepath.Join(t.TempDir(), "cases.json")
	if err := os.WriteFile(path, []byte(`[{"name":"submit","pack_id":"orders","instruction":"Submit","request_id":"fixed-request"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--server", server.URL, "--key-env", "EVALUATION_TEST_KEY", "--cases", path, "--repeat", "3"}
	var output bytes.Buffer
	passed, err := run(args, &output)
	if err != nil || passed {
		t.Fatal(passed, err, output.String())
	}
	var report struct {
		Repeat  int                         `json:"repeat"`
		Results []agenstra.EvaluationResult `json:"results"`
		Summary evaluationSummary           `json:"summary"`
	}
	if err := json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if len(ids) != 3 || len(report.Results) != 3 || report.Repeat != 3 || report.Summary.Planned != 3 || report.Summary.Executed != 3 || report.Summary.Passed != 2 || report.Summary.PassRate != 2.0/3 || report.Summary.StatusCounts["failed"] != 1 || report.Summary.FindingCounts["business_failed"] != 3 {
		t.Fatal(report, ids)
	}
	seen := map[string]bool{}
	for i, result := range report.Results {
		if result.Iteration != i+1 || result.RunID != ids[i] || result.RequestID == "fixed-request" || seen[result.RequestID] || result.RequestID == "" {
			t.Fatal(result, requests)
		}
		seen[result.RequestID] = true
		if result.RequestID != requests[i] {
			t.Fatal(result, requests)
		}
	}
	for _, repeat := range []string{"0", "101"} {
		output.Reset()
		if _, err := run(append(append([]string{}, args[:len(args)-2]...), "--repeat", repeat), &output); err == nil {
			t.Fatal("accepted invalid repeat", repeat)
		}
	}
	if err := os.WriteFile(path, []byte(`[{"name":"existing","run_id":"`+ids[0]+`"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	output.Reset()
	if _, err := run(args, &output); err == nil || len(ids) != 3 {
		t.Fatal("repeated an existing run", err, ids)
	}
}

func TestEvaluationUnconfirmedCreationReportsIdentityWithoutCancelling(t *testing.T) {
	var creates, cancels atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/cancel") {
			cancels.Add(1)
		}
		creates.Add(1)
		if _, callErr9 := w.Write([]byte(`{"run_id":`)); callErr9 != nil {
			t.Error(callErr9)
		}
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
					if callErr10 := json.NewEncoder(w).Encode(agenstra.RunDiagnostics{RunID: id, Status: status}); callErr10 != nil {
						t.Error(callErr10)
					}
					return
				}
				if callErr11 := json.NewEncoder(w).Encode(agenstra.StoredRun{RunID: id, PackID: "orders", Status: status, State: agenstra.JSON{"runtime": agenstra.RuntimeState{RunID: id, Status: status, Facts: []agenstra.Fact{{FactID: factID, SourceCapability: "orders.get", Value: agenstra.JSON{"id": json.Number("9007199254740993")}}}, Observations: []agenstra.Observation{{Capability: "orders.get", Status: "succeeded"}}, Decisions: []agenstra.JSON{{"kind": "final", "fact_ids": []string{factID}}}}}}); callErr11 != nil {
					t.Error(callErr11)
				}
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
		if callErr12 := json.NewEncoder(w).Encode(agenstra.StoredRun{RunID: id, Status: "running"}); callErr12 != nil {
			t.Error(callErr12)
		}
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
		raw, callErr13 := json.Marshal([]agenstra.EvaluationCase{c})
		if callErr13 != nil {
			t.Error(callErr13)
		}
		if callErr14 := os.WriteFile(path, raw, 0600); callErr14 != nil {
			t.Error(callErr14)
		}
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
