package agenstra

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
)

type memoryTestModel struct {
	*hostModel
	extract func(MemoryExtractionRequest) ([]MemoryProposal, error)
	inputs  []string
}

func (m *memoryTestModel) ExtractMemories(_ context.Context, r MemoryExtractionRequest) ([]MemoryProposal, error) {
	m.inputs = append(m.inputs, r.Text)
	return m.extract(r)
}
func memoryTestHost(t *testing.T, model *memoryTestModel) (*AgentHost, *SQLiteStore) {
	t.Helper()
	s := testStore(t)
	h := testHost(t, s, &hostProvider{}, model.hostModel)
	h.Model = model
	return h, s
}
func memoryRun(t *testing.T, h *AgentHost, owner, pack, text, request string) StoredRun {
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
func memoryProposal(r MemoryExtractionRequest, scope, key, value, mode string) []MemoryProposal {
	return []MemoryProposal{{Scope: scope, Key: key, Value: value, Kind: "preference", Mode: mode, Quote: r.Text}}
}
func TestMemoryHabitsAutomaticallyBecomeDefaultsAndReplayDoesNotCount(t *testing.T) {
	model := &memoryTestModel{hostModel: &hostModel{}, extract: func(r MemoryExtractionRequest) ([]MemoryProposal, error) {
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
	items, _ := h.ListMemories(t.Context(), "alice", "records", 100, 0)
	history, err := h.MemoryHistory(t.Context(), "alice", "records", items[0].ID)
	if err != nil || len(history.Evidence) != 3 || history.Evidence[0].Quote == "" {
		t.Fatal("lost provenance", history, err)
	}
	reopened, err := NewSQLiteStore(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err = reopened.Initialize(); err != nil {
		t.Fatal(err)
	}
	h2 := testHost(t, reopened, &hostProvider{}, &hostModel{hook: func(p ContextPacket) {
		if len(p.Memories) != 1 || p.Memories[0].Value != "zh-CN" {
			t.Fatal("restart forgot default", p.Memories)
		}
	}})
	memoryRun(t, h2, "alice", "records", "读取新的记录", "after-restart")
}
func TestMemoryExplicitCorrectionTemporaryExceptionsAndForget(t *testing.T) {
	model := &memoryTestModel{hostModel: &hostModel{}, extract: func(r MemoryExtractionRequest) ([]MemoryProposal, error) {
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
	items, _ := h.ListMemories(t.Context(), "alice", "records", 100, 0)
	if len(items) != 1 || items[0].Value != "zh-CN" || items[0].Origin != "explicit" {
		t.Fatal("temporary input changed default", items)
	}
	memoryRun(t, h, "alice", "records", "以后报告改用英文", "correction")
	items, _ = h.ListMemories(t.Context(), "alice", "records", 100, 0)
	if items[0].Value != "en" || items[0].Revision <= 2 {
		t.Fatal("correction not applied", items)
	}
	views, err := h.runMemories(first)
	if err != nil || len(views) != 0 {
		t.Fatal("outdated snapshot still visible", views, err)
	}
	memoryRun(t, h, "alice", "records", "忘记报告语言偏好", "forget")
	items, _ = h.ListMemories(t.Context(), "alice", "records", 100, 0)
	if items[0].Status != "forgotten" || items[0].Value != "" || items[0].Quote != "" || items[0].Origin != "explicit" {
		t.Fatal("forgotten contents retained", items)
	}
	history, err := h.MemoryHistory(t.Context(), "alice", "records", items[0].ID)
	if err != nil || len(history.Evidence) != 0 || len(history.Revisions) != 1 || history.Revisions[0].Value != "" {
		t.Fatal("forget retained old memory data", history, err)
	}
}
func TestMemoryHostManagementIsolationPrecedenceAndConcurrentRevisions(t *testing.T) {
	h, _ := memoryTestHost(t, &memoryTestModel{hostModel: &hostModel{}, extract: func(MemoryExtractionRequest) ([]MemoryProposal, error) { return nil, nil }})
	global, err := h.SetMemory(t.Context(), "alice", "records", MemoryUpdate{Scope: "user", Key: "report.language", Value: "zh-CN", Kind: "preference"})
	if err != nil {
		t.Fatal(err)
	}
	project, err := h.SetMemory(t.Context(), "alice", "records", MemoryUpdate{Scope: "pack", Key: "report.language", Value: "en", Kind: "preference"})
	if err != nil {
		t.Fatal(err)
	}
	items, _ := h.ListMemories(t.Context(), "alice", "records", 100, 0)
	if got := memoryViews(items); len(got) != 1 || got[0].Value != "en" {
		t.Fatal("project default did not take precedence", got)
	}
	items, _ = h.ListMemories(t.Context(), "alice", "other", 100, 0)
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
		if e := method(); ErrorCode(e) != "not_found" {
			t.Fatal("ownership not enforced", e)
		}
	}
	var wg sync.WaitGroup
	out := make(chan error, 2)
	for _, value := range []string{"de", "fr"} {
		wg.Go(func() {
			_, e := h.SetMemory(t.Context(), "alice", "records", MemoryUpdate{Scope: "pack", Key: project.Key, Value: value, Kind: "preference", Revision: project.Revision})
			out <- e
		})
	}
	wg.Wait()
	close(out)
	success, conflict := 0, 0
	for e := range out {
		if e == nil {
			success++
		} else if ErrorCode(e) == "revision_conflict" {
			conflict++
		} else {
			t.Fatal(e)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatal("lost update", success, conflict)
	}
	h.PolicyResolver = func(context.Context, string, string) (ExecutionPolicy, error) {
		return ExecutionPolicy{}, hostError("access_denied")
	}
	if _, e := h.ListMemories(t.Context(), "alice", "records", 100, 0); ErrorCode(e) != "access_denied" {
		t.Fatal("revoked pack remained visible", e)
	}
}
func TestMemoryManualDefaultsResistHabitAndForgettingResetsEvidence(t *testing.T) {
	model := &memoryTestModel{hostModel: &hostModel{}, extract: func(r MemoryExtractionRequest) ([]MemoryProposal, error) {
		return memoryProposal(r, "user", "report.language", "en", "habit"), nil
	}}
	h, _ := memoryTestHost(t, model)
	current, err := h.SetMemory(t.Context(), "alice", "records", MemoryUpdate{Scope: "user", Key: "report.language", Value: "zh-CN", Kind: "preference"})
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
		current, _ = h.GetMemory(t.Context(), "alice", "records", current.ID)
		if i < 2 && current.Status == "active" || i == 2 && (current.Status != "active" || current.Value != "en") {
			t.Fatal("old evidence reactivated forgotten default", current)
		}
	}
}
func TestMemoryExtractionFailuresAndInvalidEvidenceDoNotBreakBusinessRun(t *testing.T) {
	for _, bad := range []string{"unavailable", "invented_quote", "inferred_constraint", "duplicate_topic"} {
		t.Run(bad, func(t *testing.T) {
			model := &memoryTestModel{hostModel: &hostModel{}, extract: func(r MemoryExtractionRequest) ([]MemoryProposal, error) {
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
			items, _ := h.ListMemories(t.Context(), "alice", "records", 100, 0)
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
func TestMemoryContextSharesBudgetAndPreservesRequiredConventions(t *testing.T) {
	runtime, state, provider := contextBudgetRuntime(t, 2200)
	for i := 0; i < 8; i++ {
		runtime.Memories = append(runtime.Memories, MemoryView{ID: NewID(), Key: fmt.Sprintf("preference.%d", i), Value: strings.Repeat("偏好", 180), Kind: "preference", Scope: "user", Revision: 1})
	}
	packet := runtime.Context(state)
	assertContextBudget(t, packet, runtime.systemPrompt(), 2200)
	if len(packet.Memories) >= 8 || len(packet.ContextOmissions) == 0 {
		t.Fatal("memory not budgeted", packet.Memories)
	}
	second := budgetContext(packet, state, 1200-len(provider.SystemPrompt())-len(memoryUsagePrompt))
	if len(second.Memories) > len(packet.Memories) {
		t.Fatal("defaults restored under increased pressure")
	}
	allOmitted := fmt.Sprintf("memories: %d defaults omitted", 8-len(second.Memories))
	if !slices.Contains(second.ContextOmissions, allOmitted) {
		t.Fatal("omission count lost across budgeting", second.ContextOmissions, allOmitted)
	}
	for i := range runtime.Memories {
		runtime.Memories[i].Kind = "convention"
	}
	model := &hostModel{}
	runtime.Model = model
	if err := runtime.Step(t.Context(), state, nil); err != nil {
		t.Fatal(err)
	}
	if state.Status != "failed" || state.ErrorCode == nil || *state.ErrorCode != "context_too_large" || model.calls != 0 {
		t.Fatal("required convention discarded", state, model.calls)
	}
}
func TestMemorySnapshotSurvivesResumeButForgetInvalidatesIt(t *testing.T) {
	h, s := memoryTestHost(t, &memoryTestModel{hostModel: &hostModel{decisions: []Decision{{Kind: "request_input", Field: "record", Prompt: "Which record?"}}}, extract: func(MemoryExtractionRequest) ([]MemoryProposal, error) { return nil, nil }})
	m, err := h.SetMemory(t.Context(), "alice", "records", MemoryUpdate{Scope: "user", Key: "report.language", Value: "zh-CN", Kind: "preference"})
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
	frozen, _ := CanonicalJSON(r.State["memory_snapshot"])
	// Adding another memory cannot change the already-published snapshot.
	if _, err = h.SetMemory(t.Context(), "alice", "records", MemoryUpdate{Scope: "user", Key: "report.format", Value: "table", Kind: "preference"}); err != nil {
		t.Fatal(err)
	}
	h2 := testHost(t, s, &hostProvider{}, &hostModel{hook: func(p ContextPacket) {
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
	after, _ := CanonicalJSON(r.State["memory_snapshot"])
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
	model := &memoryTestModel{hostModel: &hostModel{decisions: []Decision{
		{Kind: "request_input", Field: "detail", Prompt: "First detail?"},
		{Kind: "request_input", Field: "detail", Prompt: "Second detail?"},
		{Kind: "request_input", Field: "detail", Prompt: "Third detail?"},
	}}, extract: func(r MemoryExtractionRequest) ([]MemoryProposal, error) {
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
		if _, err = h.SupplyInput(t.Context(), run.RunID, "alice", "detail", text, revision); ErrorCode(err) != "revision_conflict" {
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

func TestMemoryChatOnlyLearnsCurrentUserMessageAndScheduledRepeatsAreOneSource(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	model := &memoryTestModel{hostModel: &hostModel{}, extract: func(r MemoryExtractionRequest) ([]MemoryProposal, error) {
		return memoryProposal(r, "user", "report.language", "zh-CN", "habit"), nil
	}}
	f.h.Model = model
	c, err := f.w.CreateConversation(t.Context(), "alice", "records")
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		text := fmt.Sprintf("用中文分析记录%d", i)
		m, err := f.w.SubmitMessage(t.Context(), "alice", c.ID, fmt.Sprint(i), text, "")
		if err != nil {
			t.Fatal(err)
		}
		if err = f.w.advanceConversation(t.Context(), "alice", c.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = f.h.Drive(t.Context(), m.RunID, "alice"); err != nil {
			t.Fatal(err)
		}
		if err = f.w.advanceConversation(t.Context(), "alice", c.ID); err != nil {
			t.Fatal(err)
		}
		if model.inputs[len(model.inputs)-1] != text {
			t.Fatal("historical/assistant text was re-learned", model.inputs)
		}
	}
	task, err := f.h.CreateSchedule(t.Context(), "alice", ScheduleRequest{Name: "report", PackID: "records", Instruction: "用中文写报告", Schedule: ScheduleSpec{Kind: "interval", IntervalSeconds: 1}})
	if err != nil {
		t.Fatal(err)
	}
	before := len(model.inputs)
	for i := 0; i < 3; i++ {
		f.now += 2
		if _, err = f.h.DispatchDueSchedules(t.Context(), 10); err != nil {
			t.Fatal(err)
		}
		history, err := f.h.ListScheduleExecutions(t.Context(), task.ScheduleID, "alice", 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range history {
			if _, err = f.h.Drive(t.Context(), e.RunID, "alice"); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(model.inputs) != before+1 {
		t.Fatal("scheduled execution counted as new user evidence", model.inputs)
	}
}
