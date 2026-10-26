package deployment

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	durablehost "github.com/KHG420/agenstra/internal/runtime/host"
	"github.com/KHG420/agenstra/internal/runtime/react"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

func TestCompletionReviewProfileFrozenAcrossRestart(t *testing.T) {
	var mu sync.Mutex
	requests := []agentcontract.JSON{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload agentcontract.JSON
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		mu.Lock()
		requests = append(requests, payload)
		mu.Unlock()
		content := `{"kind":"final","answer_markdown":"ok","fact_ids":[]}`
		switch {
		case strings.HasPrefix(payload["model"].(string), "memory"):
			content = `{"proposals":[]}`
		case strings.HasPrefix(payload["model"].(string), "review"):
			content = `{"kind":"approve_completion"}`
		}
		memoryModelReply(t, w, content)
	}))
	defer server.Close()
	d := selectionTestDeployment(t, server.URL)
	business := d.Config.Models.Profiles["business"]
	business.Thinking, business.ReasoningEffort, business.MaxOutputTokens = "disabled", "", 64
	business.ContextWindowTokens = 100000
	d.Config.Models.Profiles["business"] = business
	review := business
	review.Model, review.Thinking, review.ReasoningEffort = "review-old", "enabled", "high"
	review.MaxOutputTokens, review.ContextWindowTokens, review.MaxInputTokens = 512, 50000, 45000
	d.Config.Models.Profiles["review"] = review
	d.Config.Models.CompletionReviewProfile = "review"
	manager, err := d.NewModel()
	if err != nil {
		t.Fatal(err)
	}
	store, err := runstore.NewSQLiteStore(d.DatabasePath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	})
	if err := store.Initialize(); err != nil {
		t.Fatal(err)
	}
	host := durablehost.NewAgentHost(store, func(context.Context, string, string) (agentcontract.CapabilityProvider, error) {
		return &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, nil
	}, manager, func(context.Context, string, string) (agentcontract.ExecutionPolicy, error) {
		return agentcontract.ExecutionPolicy{AllowModelData: true}, nil
	})
	host.Settings.MaxModelOutputTokens = 1024
	host.CompletionReviewPolicy = func(context.Context, agentcontract.CompletionContext) bool { return true }
	oldRun, err := host.Create(t.Context(), "alice", "project", "reply ok", "old")
	if err != nil {
		t.Fatal(err)
	}
	config := manager.Snapshot().Config
	for id, profile := range config.Profiles {
		profile.Model = strings.ReplaceAll(profile.Model, "old", "new")
		if id == "review" {
			profile.ReasoningEffort = "low"
		}
		config.Profiles[id] = profile
	}
	if _, err := manager.Configure(config, 0); err != nil {
		t.Fatal(err)
	}
	if err := d.Registry.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := d.NewModel()
	if err != nil {
		t.Fatal(err)
	}
	restored := durablehost.NewAgentHost(store, host.ProviderFactory, restarted, host.PolicyResolver)
	restored.Settings = host.Settings
	restored.CompletionReviewPolicy = host.CompletionReviewPolicy
	newRun, err := restored.Create(t.Context(), "alice", "project", "reply ok", "new")
	if err != nil {
		t.Fatal(err)
	}
	for _, run := range []agentcontract.StoredRun{oldRun, newRun} {
		result, err := restored.Drive(t.Context(), run.RunID, "alice")
		if err != nil || result.Status != "completed" {
			t.Fatal("frozen review failed", result.Status, result.State["runtime"], err)
		}
		telemetry, err := restored.GetTelemetry(t.Context(), run.RunID, "alice")
		if err != nil || telemetry.Budget.UsageByPurpose["decision"].Requests != 1 || telemetry.Budget.UsageByPurpose["memory_extraction"].Requests != 1 || telemetry.Budget.UsageByPurpose["completion_review"].Requests != 1 {
			t.Fatal("phase usage changed", err, telemetry.Budget)
		}
		state, err := restored.Restore(result)
		if err != nil || state.ContextTelemetry == nil || *state.ContextTelemetry.ModelContextWindowTokens != 50000 || *state.ContextTelemetry.EffectiveInputTokenLimit != 45000 || *state.ContextTelemetry.ReservedOutputTokens != 512 {
			t.Fatal("review used decision capacity instead of its frozen profile", err, state)
		}
	}
	mu.Lock()
	got := append([]agentcontract.JSON{}, requests...)
	mu.Unlock()
	wantModels := []string{"memory-old", "business-old", "review-old", "memory-new", "business-new", "review-new"}
	if len(got) != len(wantModels) {
		t.Fatal("unexpected request count", len(got))
	}
	for i, wantModel := range wantModels {
		payload := got[i]
		if payload["model"] != wantModel {
			t.Fatal("run rerouted after catalog update", payload["model"], wantModel)
		}
		thinking := payload["thinking"].(map[string]any)["type"]
		if i%3 == 2 {
			wantEffort := "high"
			if i == 5 {
				wantEffort = "low"
			}
			if thinking != "enabled" || payload["reasoning_effort"] != wantEffort || payload["max_tokens"] != float64(512) {
				t.Fatal("review parameters were not frozen or used decision ceiling", payload)
			}
		} else if thinking != "disabled" || payload["reasoning_effort"] != nil || payload["max_tokens"] != float64(64) {
			t.Fatal("review settings leaked to ordinary decision or memory", payload)
		}
	}
}

func TestCompletionReviewProfileMeasurementMatchesExecution(t *testing.T) {
	for _, stage := range []string{"decision", "review", "inherited_review"} {
		t.Run(stage, func(t *testing.T) {
			requests := make(chan []byte, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, err := io.ReadAll(r.Body)
				if err != nil {
					t.Error(err)
				}
				requests <- raw
				arguments := `{"kind":"final","answer_markdown":"ok","fact_ids":[]}`
				if stage != "decision" {
					arguments = `{"kind":"approve_completion"}`
				}
				if err := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{"message": agentcontract.JSON{"tool_calls": []any{agentcontract.JSON{"type": "function", "function": agentcontract.JSON{"name": "submit_decision", "arguments": arguments}}}}}}}); err != nil {
					t.Error(err)
				}
			}))
			defer server.Close()
			d := selectionTestDeployment(t, server.URL)
			d.Config.Models.DefaultProfile, d.Config.Models.DecisionProfile = "memory", "business"
			business := d.Config.Models.Profiles["business"]
			business.Thinking, business.ReasoningEffort, business.DecisionOutputMode = "disabled", "", "output_tools"
			d.Config.Models.Profiles["business"] = business
			review := business
			review.Thinking, review.ReasoningEffort, review.Model = "enabled", "high", "review-model"
			d.Config.Models.Profiles["review"] = review
			if stage != "inherited_review" {
				d.Config.Models.CompletionReviewProfile = "review"
			}
			manager, err := d.NewModel()
			if err != nil {
				t.Fatal(err)
			}
			packet := agentcontract.ContextPacket{Schema: "agenstra.context.v1", Instruction: "reply ok"}
			if stage != "decision" {
				packet.CompletionReview = &agentcontract.Decision{Kind: "final", AnswerMarkdown: "ok"}
			}
			measured, err := manager.MeasureInput(packet, "Return a valid decision.")
			if err != nil {
				t.Fatal(err)
			}
			decision, err := manager.Decide(t.Context(), packet, "Return a valid decision.")
			if err != nil {
				t.Fatal(err)
			}
			raw := <-requests
			wantProfile := "business"
			if stage == "review" {
				wantProfile = "review"
			}
			if measured.Tokens != int64(len(raw)+128) || decision.ModelCall.Profile != wantProfile {
				t.Fatal("measurement or execution selected the wrong profile", measured.Tokens, len(raw), decision.ModelCall)
			}
			if stage == "review" {
				var payload struct {
					Thinking struct{ Type string }
					Messages []struct{ Role, Content string }
				}
				if err := json.Unmarshal(raw, &payload); err != nil {
					t.Fatal(err)
				}
				var retained agentcontract.ContextPacket
				if len(payload.Messages) != 2 || payload.Thinking.Type != "enabled" || payload.Messages[1].Role != "user" || json.Unmarshal([]byte(payload.Messages[1].Content), &retained) != nil || retained.CompletionReview == nil || retained.CompletionReview.AnswerMarkdown != "ok" {
					t.Fatal("thinking review lost its data-only frozen proposal")
				}
			}
		})
	}
}

func TestCompletionReviewProfileHonorsSmallerCapacity(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests++
		memoryModelReply(t, w, `{"kind":"final","answer_markdown":"draft","fact_ids":[]}`)
	}))
	defer server.Close()
	d := selectionTestDeployment(t, server.URL)
	review := d.Config.Models.Profiles["business"]
	review.MaxInputTokens = 1
	d.Config.Models.Profiles["review"] = review
	d.Config.Models.CompletionReviewProfile = "review"
	manager, err := d.NewModel()
	if err != nil {
		t.Fatal(err)
	}
	runtime := &react.AgentRuntime{Provider: &coreTestProvider{caps: map[string]agentcontract.CapabilityDescription{}}, Model: manager, CompletionReviewPolicy: func(context.Context, agentcontract.CompletionContext) bool { return true }}
	state, err := runtime.NewState("reply ok", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	if state.Status != "failed" || state.ErrorCode == nil || *state.ErrorCode != "context_too_large" || state.AnswerMarkdown != "" || requests != 1 {
		t.Fatal("review dispatched beyond its capacity or published an unchecked draft", requests, state)
	}
}
