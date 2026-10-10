package host

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/state/runstore"
)

type memoryTestModel struct {
	*hostModel
	extract func(agentcontract.MemoryExtractionRequest) ([]agentcontract.MemoryProposal, error)
	inputs  []string
}

func (m *memoryTestModel) ExtractMemories(_ context.Context, r agentcontract.MemoryExtractionRequest) ([]agentcontract.MemoryProposal, error) {
	m.inputs = append(m.inputs, r.Text)
	return m.extract(r)
}

func memoryTestHost(t *testing.T, model *memoryTestModel) (*AgentHost, *runstore.SQLiteStore) {
	t.Helper()
	s := testStore(t)
	h := testHost(t, s, &hostProvider{}, model.hostModel)
	h.Model = model
	return h, s
}

func memoryRun(t *testing.T, h *AgentHost, owner, pack, text, request string) agentcontract.StoredRun {
	t.Helper()
	run, err := h.Create(t.Context(), owner, pack, text, request)
	if err != nil {
		t.Fatal(err)
	}
	run, err = h.Drive(t.Context(), run.RunID, owner)
	if err != nil || run.Status != "completed" {
		t.Fatalf("run: %s %v", run.Status, err)
	}
	return run
}

func memoryProposal(r agentcontract.MemoryExtractionRequest, scope, key, value, mode string) []agentcontract.MemoryProposal {
	return []agentcontract.MemoryProposal{{Scope: scope, Key: key, Value: value, Kind: "preference", Mode: mode, Quote: r.Text}}
}

func TestMemoryExtractionTelemetryDoesNotExposeUncodedErrors(t *testing.T) {
	model := &memoryTestModel{hostModel: &hostModel{}, extract: func(agentcontract.MemoryExtractionRequest) ([]agentcontract.MemoryProposal, error) {
		return nil, errors.New("private endpoint and token details")
	}}
	host, _ := memoryTestHost(t, model)
	run := memoryRun(t, host, "alice", "records", "use Chinese", "safe-memory-error")
	state, err := host.Restore(run)
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range state.ModelCalls {
		if call.Purpose == "memory_extraction" {
			if call.ErrorCode == nil || *call.ErrorCode != "model_unavailable" {
				t.Fatalf("memory telemetry leaked uncoded error: %+v", call)
			}
			return
		}
	}
	t.Fatal("memory extraction telemetry missing")
}

func TestMemoryHabitsAutomaticallyBecomeDefaultsAndReplayDoesNotCount(t *testing.T) {
	model := &memoryTestModel{hostModel: &hostModel{}, extract: func(r agentcontract.MemoryExtractionRequest) ([]agentcontract.MemoryProposal, error) {
		return memoryProposal(r, "user", "report.language", "zh-CN", "habit"), nil
	}}
	h, s := memoryTestHost(t, model)
	for i := 0; i < 3; i++ {
		request := fmt.Sprint(i)
		text := fmt.Sprintf("请用中文写报告，内容是第%d份记录", i)
		run := memoryRun(t, h, "alice", "records", text, request)
		items, err := h.ListMemories(t.Context(), "alice", "records", 100, 0)
		if err != nil || len(items) != 1 || items[0].EvidenceCount != i+1 {
			t.Fatal(items, err)
		}
		if i < 2 && items[0].Status != "candidate" || i == 2 && items[0].Status != "active" {
			t.Fatal("incorrect adoption", items)
		}
		views, err := h.runMemories(run)
		if err != nil || i < 2 && len(views) != 0 || i == 2 && (len(views) != 1 || views[0].Value != "zh-CN") {
			t.Fatal("projection", views, err)
		}
		memoryRun(t, h, "alice", "records", text, request)
	}
	if len(model.inputs) != 3 {
		t.Fatal("replays were extracted again", model.inputs)
	}
	items, callErr := h.ListMemories(t.Context(), "alice", "records", 100, 0)
	if callErr != nil {
		t.Error(callErr)
	}
	history, err := h.MemoryHistory(t.Context(), "alice", "records", items[0].ID)
	if err != nil || len(history.Evidence) != 3 || history.Evidence[0].Quote == "" {
		t.Fatal("lost provenance", history, err)
	}
	reopened, err := runstore.NewSQLiteStore(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(reopened.Close)
	if err = reopened.Initialize(); err != nil {
		t.Fatal(err)
	}
	h2 := testHost(t, reopened, &hostProvider{}, &hostModel{hook: func(p agentcontract.ContextPacket) {
		if len(p.Memories) != 1 || p.Memories[0].Value != "zh-CN" {
			t.Fatal("restart forgot default", p.Memories)
		}
	}})
	memoryRun(t, h2, "alice", "records", "读取新的记录", "after-restart")
}

func TestMemoryExplicitCorrectionTemporaryExceptionsAndForget(t *testing.T) {
	model := &memoryTestModel{hostModel: &hostModel{}, extract: func(r agentcontract.MemoryExtractionRequest) ([]agentcontract.MemoryProposal, error) {
		mode, value := "explicit", "zh-CN"
		if strings.Contains(r.Text, "英文") {
			value = "en"
		}
		if strings.Contains(r.Text, "这次") {
			mode = "habit"
		} // deliberately misclassified
		if strings.Contains(r.Text, "忘记") {
			mode = "forget"
		}
		return memoryProposal(r, "user", "report.language", value, mode), nil
	}}
	h, _ := memoryTestHost(t, model)
	first := memoryRun(t, h, "alice", "records", "以后报告使用中文", "first")
	for i := 0; i < 3; i++ {
		memoryRun(t, h, "alice", "records", "这次报告用英文", fmt.Sprintf("temporary-%d", i))
	}
	items, callErr2 := h.ListMemories(t.Context(), "alice", "records", 100, 0)
	if callErr2 != nil {
		t.Error(callErr2)
	}
	if len(items) != 1 || items[0].Value != "zh-CN" || items[0].Origin != "explicit" {
		t.Fatal("temporary input changed default", items)
	}
	memoryRun(t, h, "alice", "records", "以后报告改用英文", "correction")
	var callErr3 error
	items, callErr3 = h.ListMemories(t.Context(), "alice", "records", 100, 0)
	if callErr3 != nil {
		t.Error(callErr3)
	}
	if items[0].Value != "en" || items[0].Revision <= 2 {
		t.Fatal("correction not applied", items)
	}
	views, err := h.runMemories(first)
	if err != nil || len(views) != 0 {
		t.Fatal("outdated snapshot still visible", views, err)
	}
	memoryRun(t, h, "alice", "records", "忘记报告语言偏好", "forget")
	var callErr4 error
	items, callErr4 = h.ListMemories(t.Context(), "alice", "records", 100, 0)
	if callErr4 != nil {
		t.Error(callErr4)
	}
	if items[0].Status != "forgotten" || items[0].Value != "" || items[0].Quote != "" || items[0].Origin != "explicit" {
		t.Fatal("forgotten contents retained", items)
	}
	history, err := h.MemoryHistory(t.Context(), "alice", "records", items[0].ID)
	if err != nil || len(history.Evidence) != 0 || len(history.Revisions) != 1 || history.Revisions[0].Value != "" {
		t.Fatal("forget retained old memory data", history, err)
	}
}

func TestMemoryOneOffExplicitProposalsDoNotChangeDefaults(t *testing.T) {
	text := "仅操作本轮专属记录 106。先单独读取一次上下文。只执行一次重置。最后仅报告实际回执的编号和状态。不输出执行过程。"
	model := &memoryTestModel{hostModel: &hostModel{}, extract: func(agentcontract.MemoryExtractionRequest) ([]agentcontract.MemoryProposal, error) {
		// Replay the real extractor failure: explicit labels for current-task
		// constraints, including quotes that omit the input's temporal qualifier.
		return []agentcontract.MemoryProposal{
			{Scope: "pack", Key: "project.operation.scope", Value: "仅操作本轮专属记录 106", Kind: "constraint", Mode: "explicit", Quote: "仅操作本轮专属记录 106"},
			{Scope: "pack", Key: "project.operation.sequence", Value: "先单独读取一次上下文", Kind: "constraint", Mode: "explicit", Quote: "先单独读取一次上下文"},
			{Scope: "pack", Key: "project.operation.api_restriction", Value: "只执行一次重置", Kind: "constraint", Mode: "explicit", Quote: "只执行一次重置"},
			{Scope: "pack", Key: "report.format", Value: "最后仅报告实际回执的编号和状态", Kind: "preference", Mode: "explicit", Quote: "最后仅报告实际回执的编号和状态"},
			{Scope: "pack", Key: "report.output.exclusions", Value: "不输出执行过程", Kind: "constraint", Mode: "explicit", Quote: "不输出执行过程"},
		}, nil
	}}
	h, _ := memoryTestHost(t, model)
	previous, err := h.SetMemory(t.Context(), "alice", "records", agentcontract.MemoryUpdate{Scope: "pack", Key: "report.format", Value: "concise", Kind: "preference"})
	if err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		run := memoryRun(t, h, "alice", "records", text, fmt.Sprintf("one-off-explicit-%d", i))
		items, err := h.ListMemories(t.Context(), "alice", "records", 100, 0)
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || items[0].ID != previous.ID || items[0].Value != previous.Value || items[0].Revision != previous.Revision {
			t.Fatalf("one-off explicit proposal changed durable defaults: %+v", items)
		}
		views, err := h.runMemories(run)
		if err != nil || len(views) != 1 || views[0].Value != previous.Value {
			t.Fatalf("one-off constraints entered the run memory snapshot: %+v %v", views, err)
		}
	}
}

func TestMemoryExplicitLastingClauseAlongsideTemporaryTask(t *testing.T) {
	for _, clause := range []string{"以后报告使用中文", "记住：报告使用中文", "From now on use Chinese reports", "Remember: use Chinese reports"} {
		t.Run(clause, func(t *testing.T) {
			model := &memoryTestModel{hostModel: &hostModel{}, extract: func(agentcontract.MemoryExtractionRequest) ([]agentcontract.MemoryProposal, error) {
				return []agentcontract.MemoryProposal{{Scope: "user", Key: "report.language", Value: "zh-CN", Kind: "preference", Mode: "explicit", Quote: clause}}, nil
			}}
			h, _ := memoryTestHost(t, model)
			memoryRun(t, h, "alice", "records", "本轮只处理记录 106。"+clause, "mixed-persistence-intent")
			items, err := h.ListMemories(t.Context(), "alice", "records", 100, 0)
			if err != nil || len(items) != 1 || items[0].Status != "active" || items[0].Value != "zh-CN" {
				t.Fatalf("explicit lasting clause was lost with temporary task: %+v %v", items, err)
			}
		})
	}
}

func TestMemoryTemporaryExplicitLabelsKeepTemporaryScope(t *testing.T) {
	for _, text := range []string{"这次报告用英文", "本次报告用英文", "Remember: this time use English", "For this task use English", "For this run use English"} {
		t.Run(text, func(t *testing.T) {
			model := &memoryTestModel{hostModel: &hostModel{}, extract: func(r agentcontract.MemoryExtractionRequest) ([]agentcontract.MemoryProposal, error) {
				return memoryProposal(r, "user", "report.language", "en", "explicit"), nil
			}}
			h, _ := memoryTestHost(t, model)
			previous, err := h.SetMemory(t.Context(), "alice", "records", agentcontract.MemoryUpdate{Scope: "user", Key: "report.language", Value: "zh-CN", Kind: "preference"})
			if err != nil {
				t.Fatal(err)
			}
			for i := range 3 {
				memoryRun(t, h, "alice", "records", text, fmt.Sprintf("temporary-explicit-%d", i))
			}
			current, err := h.GetMemory(t.Context(), "alice", "records", previous.ID)
			if err != nil || current.Value != previous.Value || current.Revision != previous.Revision {
				t.Fatalf("temporary explicit label changed the default: %+v %v", current, err)
			}
			history, err := h.MemoryHistory(t.Context(), "alice", "records", previous.ID)
			if err != nil || len(history.Evidence) != 0 || len(history.Revisions) != 1 {
				t.Fatalf("temporary input accumulated durable evidence: %+v %v", history, err)
			}
		})
	}
}

func TestMemoryHostManagementIsolationPrecedenceAndConcurrentRevisions(t *testing.T) {
	h, _ := memoryTestHost(t, &memoryTestModel{hostModel: &hostModel{}, extract: func(agentcontract.MemoryExtractionRequest) ([]agentcontract.MemoryProposal, error) { return nil, nil }})
	global, err := h.SetMemory(t.Context(), "alice", "records", agentcontract.MemoryUpdate{Scope: "user", Key: "report.language", Value: "zh-CN", Kind: "preference"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := h.SetMemory(t.Context(), "alice", "records", agentcontract.MemoryUpdate{Scope: "pack", Key: "report.language", Value: "en", Kind: "preference"})
	if err != nil {
		t.Fatal(err)
	}
	items, callErr5 := h.ListMemories(t.Context(), "alice", "records", 100, 0)
	if callErr5 != nil {
		t.Error(callErr5)
	}
	if got := memoryViews(items); len(got) != 1 || got[0].Value != "en" {
		t.Fatal("project default did not take precedence", got)
	}
	var callErr6 error
	items, callErr6 = h.ListMemories(t.Context(), "alice", "other", 100, 0)
	if callErr6 != nil {
		t.Error(callErr6)
	}
	if got := memoryViews(items); len(got) != 1 || got[0].Value != "zh-CN" {
		t.Fatal("project leaked", got)
	}
	for _, method := range []func() error{
		func() error { _, e := h.GetMemory(t.Context(), "bob", "records", global.ID); return e },
		func() error { _, e := h.GetMemory(t.Context(), "alice", "other", project.ID); return e },
		func() error {
			_, e := h.DeleteMemory(t.Context(), "bob", "records", global.ID, global.Revision)
			return e
		},
		func() error { _, e := h.MemoryHistory(t.Context(), "bob", "records", global.ID); return e },
	} {
		if e := method(); agentcontract.ErrorCode(e) != "not_found" {
			t.Fatal("ownership not enforced", e)
		}
	}
	var wg sync.WaitGroup
	out := make(chan error, 2)
	for _, value := range []string{"de", "fr"} {
		wg.Go(func() {
			_, e := h.SetMemory(t.Context(), "alice", "records", agentcontract.MemoryUpdate{Scope: "pack", Key: project.Key, Value: value, Kind: "preference", Revision: project.Revision})
			out <- e
		})
	}
	wg.Wait()
	close(out)
	success, conflict := 0, 0
	for e := range out {
		if e == nil {
			success++
		} else if agentcontract.ErrorCode(e) == "revision_conflict" {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("lost update", success, conflict)
	}
	h.PolicyResolver = func(context.Context, string, string) (agentcontract.ExecutionPolicy, error) {
		return agentcontract.ExecutionPolicy{}, agentcontract.NewHostError("access_denied")
	}
	if _, e := h.ListMemories(t.Context(), "alice", "records", 100, 0); agentcontract.ErrorCode(e) != "access_denied" {
		t.Fatal("revoked pack remained visible", e)
	}
}

func TestMemoryManualDefaultsResistHabitAndForgettingResetsEvidence(t *testing.T) {
	model := &memoryTestModel{hostModel: &hostModel{}, extract: func(r agentcontract.MemoryExtractionRequest) ([]agentcontract.MemoryProposal, error) {
		return memoryProposal(r, "user", "report.language", "en", "habit"), nil
	}}
	h, _ := memoryTestHost(t, model)
	current, err := h.SetMemory(t.Context(), "alice", "records", agentcontract.MemoryUpdate{Scope: "user", Key: "report.language", Value: "zh-CN", Kind: "preference"})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		memoryRun(t, h, "alice", "records", "Write an English report", fmt.Sprintf("prior-%d", i))
	}
	current, err = h.GetMemory(t.Context(), "alice", "records", current.ID)
	if err != nil || current.Value != "zh-CN" {
		t.Fatal("manual default overwritten", current, err)
	}
	if _, err = h.DeleteMemory(t.Context(), "alice", "records", current.ID, current.Revision); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		memoryRun(t, h, "alice", "records", "Write an English report", fmt.Sprintf("new-%d", i))
		var callErr7 error
		current, callErr7 = h.GetMemory(t.Context(), "alice", "records", current.ID)
		if callErr7 != nil {
			t.Error(callErr7)
		}
		if i < 2 && current.Status == "active" || i == 2 && (current.Status != "active" || current.Value != "en") {
			t.Fatal("old evidence reactivated forgotten default", current)
		}
	}
}

func TestMemoryExtractionExcludesForgottenDefaultsBeforeItsLimit(t *testing.T) {
	calls := 0
	model := &memoryTestModel{hostModel: &hostModel{}, extract: func(r agentcontract.MemoryExtractionRequest) ([]agentcontract.MemoryProposal, error) {
		calls++
		if calls == 1 {
			return memoryProposal(r, "user", "report.format", "table", "habit"), nil
		}
		keys := map[string]bool{}
		for _, item := range r.Existing {
			if strings.HasPrefix(item.Key, "obsolete.") {
				t.Fatal("forgotten default sent to the extractor", item)
			}
			keys[item.Key] = true
		}
		if len(keys) != 2 || !keys["report.language"] || !keys["report.format"] {
			t.Fatal("forgotten defaults crowded out active and candidate entries", keys)
		}
		return nil, nil
	}}
	h, _ := memoryTestHost(t, model)
	memoryRun(t, h, "alice", "records", "Use a table for the report", "candidate")
	if _, err := h.SetMemory(t.Context(), "alice", "records", agentcontract.MemoryUpdate{Scope: "user", Key: "report.language", Value: "en", Kind: "preference"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 33; i++ {
		item, err := h.SetMemory(t.Context(), "alice", "records", agentcontract.MemoryUpdate{Scope: "user", Key: fmt.Sprintf("obsolete.preference_%d", i), Value: "old", Kind: "preference"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = h.DeleteMemory(t.Context(), "alice", "records", item.ID, item.Revision); err != nil {
			t.Fatal(err)
		}
	}
	memoryRun(t, h, "alice", "records", "Read a fresh record", "inspect")
	if calls != 2 {
		t.Fatal("unexpected extraction count", calls)
	}
}

func TestMemoryExtractionFailuresAndInvalidEvidenceDoNotBreakBusinessRun(t *testing.T) {
	for _, bad := range []string{"unavailable", "invented_quote", "inferred_constraint", "duplicate_topic"} {
		t.Run(bad, func(t *testing.T) {
			model := &memoryTestModel{hostModel: &hostModel{}, extract: func(r agentcontract.MemoryExtractionRequest) ([]agentcontract.MemoryProposal, error) {
				p := memoryProposal(r, "user", "report.language", "zh-CN", "habit")
				switch bad {
				case "unavailable":
					return nil, errors.New("model unavailable")
				case "invented_quote":
					p[0].Quote = "invented"
				case "inferred_constraint":
					p[0].Kind = "constraint"
				case "duplicate_topic":
					p = append(p, p[0])
				}
				return p, nil
			}}
			h, _ := memoryTestHost(t, model)
			r := memoryRun(t, h, "alice", "records", "请用中文写报告", "one")
			if errs, ok := r.State["memory_errors"].([]any); !ok || len(errs) != 1 {
				t.Fatal("learning failure hidden", r.State["memory_errors"])
			}
			items, callErr8 := h.ListMemories(t.Context(), "alice", "records", 100, 0)
			if callErr8 != nil {
				t.Error(callErr8)
			}
			if len(items) != 0 {
				t.Fatal("invalid evidence was persisted", items)
			}
			memoryRun(t, h, "alice", "records", "请用中文写报告", "one")
			if len(model.inputs) != 1 {
				t.Fatal("failure retried indefinitely", model.inputs)
			}
		})
	}
}

func TestMemorySnapshotSurvivesResumeButForgetInvalidatesIt(t *testing.T) {
	h, s := memoryTestHost(t, &memoryTestModel{hostModel: &hostModel{decisions: []agentcontract.Decision{{Kind: "request_input", Field: "record", Prompt: "Which record?"}}}, extract: func(agentcontract.MemoryExtractionRequest) ([]agentcontract.MemoryProposal, error) { return nil, nil }})
	m, err := h.SetMemory(t.Context(), "alice", "records", agentcontract.MemoryUpdate{Scope: "user", Key: "report.language", Value: "zh-CN", Kind: "preference"})
	if err != nil {
		t.Fatal(err)
	}
	r, err := h.Create(t.Context(), "alice", "records", "Write a report", "snap")
	if err != nil {
		t.Fatal(err)
	}
	r, err = h.Drive(t.Context(), r.RunID, "alice")
	if err != nil || r.Status != "needs_input" {
		t.Fatal(r, err)
	}
	frozen, callErr9 := agentcontract.CanonicalJSON(r.State["memory_snapshot"])
	if callErr9 != nil {
		t.Error(callErr9)
	}

	// Adding another memory cannot change the already-published snapshot.
	if _, err = h.SetMemory(t.Context(), "alice", "records", agentcontract.MemoryUpdate{Scope: "user", Key: "report.format", Value: "table", Kind: "preference"}); err != nil {
		t.Fatal(err)
	}
	h2 := testHost(t, s, &hostProvider{}, &hostModel{hook: func(p agentcontract.ContextPacket) {
		if len(p.Memories) != 1 || p.Memories[0].ID != m.ID {
			t.Fatal("snapshot changed on restart", p.Memories)
		}
	}})
	r, err = h2.SupplyInput(t.Context(), r.RunID, "alice", "record", "R-1", r.Revision)
	if err != nil {
		t.Fatal(err)
	}
	r, err = h2.Drive(t.Context(), r.RunID, "alice")
	if err != nil || r.Status != "completed" {
		t.Fatal(r.Status, err)
	}
	after, callErr10 := agentcontract.CanonicalJSON(r.State["memory_snapshot"])
	if callErr10 != nil {
		t.Error(callErr10)
	}
	if string(frozen) != string(after) {
		t.Fatal("saved memory snapshot changed")
	}
	if _, err = h2.DeleteMemory(t.Context(), "alice", "records", m.ID, m.Revision); err != nil {
		t.Fatal(err)
	}
	if views, err := h2.runMemories(r); err != nil || len(views) != 0 {
		t.Fatal("forgotten snapshot was still recalled", views, err)
	}
}

func TestMemoryFollowupsLearnOnceWithoutChangingRunningSnapshot(t *testing.T) {
	model := &memoryTestModel{hostModel: &hostModel{decisions: []agentcontract.Decision{
		{Kind: "request_input", Field: "detail", Prompt: "First detail?"},
		{Kind: "request_input", Field: "detail", Prompt: "Second detail?"},
		{Kind: "request_input", Field: "detail", Prompt: "Third detail?"},
	}}, extract: func(r agentcontract.MemoryExtractionRequest) ([]agentcontract.MemoryProposal, error) {
		if strings.Contains(r.Text, "中文") {
			return memoryProposal(r, "user", "report.language", "zh-CN", "habit"), nil
		}
		return nil, nil
	}}
	h, _ := memoryTestHost(t, model)
	run, err := h.Create(t.Context(), "alice", "records", "读取记录", "followups")
	if err != nil {
		t.Fatal(err)
	}
	run, err = h.Drive(t.Context(), run.RunID, "alice")
	if err != nil || run.Status != "needs_input" {
		t.Fatal(run.Status, err)
	}
	for i := 0; i < 3; i++ {
		text, revision := fmt.Sprintf("用中文写第%d部分", i), run.Revision
		run, err = h.SupplyInput(t.Context(), run.RunID, "alice", "detail", text, revision)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = h.SupplyInput(t.Context(), run.RunID, "alice", "detail", text, revision); agentcontract.ErrorCode(err) != "revision_conflict" {
			t.Fatal("replayed followup accepted", err)
		}
		run, err = h.Drive(t.Context(), run.RunID, "alice")
		if err != nil {
			t.Fatal(err)
		}
	}
	if run.Status != "completed" || len(model.inputs) != 4 {
		t.Fatal(run.Status, model.inputs)
	}
	items, err := h.ListMemories(t.Context(), "alice", "records", 100, 0)
	if err != nil || len(items) != 1 || items[0].Status != "active" || items[0].EvidenceCount != 3 {
		t.Fatal("followups did not become independent evidence", items, err)
	}
	if views, err := h.runMemories(run); err != nil || len(views) != 0 {
		t.Fatal("running snapshot changed", views, err)
	}
	next := memoryRun(t, h, "alice", "records", "读取其他记录", "next")
	if views, err := h.runMemories(next); err != nil || len(views) != 1 || views[0].Value != "zh-CN" {
		t.Fatal("followup preference unavailable in next run", views, err)
	}
}
