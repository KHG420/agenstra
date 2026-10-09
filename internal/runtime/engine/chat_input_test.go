package engine

import "testing"

func TestChatDirectReplyCompletesWithoutTools(t *testing.T) {
	f := newWebFixture(t, &hostModel{decisions: []Decision{{Schema: "agenstra.decision.v1", Kind: "final", AnswerMarkdown: "你好！"}}}, false)
	c, e := f.w.CreateConversation(t.Context(), "alice", "records")
	if e != nil {
		t.Fatal(e)
	}
	m, e := f.w.SubmitMessage(t.Context(), "alice", c.ID, "hello", "你好", "")
	if e != nil {
		t.Fatal(e)
	}
	if e = f.w.Tick(t.Context()); e != nil {
		t.Fatal(e)
	}
	run, e := f.h.Drive(t.Context(), m.RunID, "alice")
	if e != nil || run.Status != "completed" {
		t.Fatal(run, e)
	}
	_, messages, e := f.w.Conversation(t.Context(), "alice", c.ID)
	if e != nil || len(messages) != 1 || messages[0].AnswerMarkdown != "你好！" {
		t.Fatal(messages, e)
	}
	state, e := f.h.restore(run)
	if e != nil || state.ToolCallsUsed != 0 || len(state.Followups) != 0 {
		t.Fatal(state, e)
	}
}

func TestChatInputHistorySurvivesCompletionAndReopen(t *testing.T) {
	f := newWebFixture(t, &hostModel{decisions: []Decision{
		{Schema: "agenstra.decision.v1", Kind: "request_input", Field: "details", Prompt: "奖项名称是什么？"},
		{Schema: "agenstra.decision.v1", Kind: "request_input", Field: "details", Prompt: "奖品、总份数和每轮人数是多少？"},
		{Schema: "agenstra.decision.v1", Kind: "final", AnswerMarkdown: "信息已收到。"},
	}}, false)
	c, e := f.w.CreateConversation(t.Context(), "alice", "records")
	if e != nil {
		t.Fatal(e)
	}
	m, e := f.w.SubmitMessage(t.Context(), "alice", c.ID, "new-prize", "新增一个奖项", "")
	if e != nil {
		t.Fatal(e)
	}
	if e = f.w.Tick(t.Context()); e != nil {
		t.Fatal(e)
	}
	run, e := f.h.Drive(t.Context(), m.RunID, "alice")
	if e != nil || run.Status != "needs_input" {
		t.Fatal(run, e)
	}
	firstRevision := run.Revision
	if _, e = f.h.SupplyInput(t.Context(), run.RunID, "alice", "details", "四等奖", run.Revision); e != nil {
		t.Fatal(e)
	}
	run, e = f.h.Drive(t.Context(), run.RunID, "alice")
	if e != nil || run.Status != "needs_input" {
		t.Fatal(run, e)
	}
	if _, e = f.h.SupplyInput(t.Context(), run.RunID, "alice", "details", "四等奖", firstRevision); ErrorCode(e) != "revision_conflict" {
		t.Fatal(e)
	}
	_, messages, e := f.w.Conversation(t.Context(), "alice", c.ID)
	if e != nil || len(messages) != 1 {
		t.Fatal(messages, e)
	}
	if len(messages[0].InputHistory) != 1 || messages[0].InputHistory[0].Prompt != "奖项名称是什么？" || messages[0].InputHistory[0].Text != "四等奖" {
		t.Fatalf("missing active input history: %+v", messages[0])
	}
	second := "奖品：红米 K70\n共12份，每轮3人"
	if _, e = f.h.SupplyInput(t.Context(), run.RunID, "alice", "details", second, run.Revision); e != nil {
		t.Fatal(e)
	}
	run, e = f.h.Drive(t.Context(), run.RunID, "alice")
	if e != nil || run.Status != "completed" {
		t.Fatal(run, e)
	}
	if err := f.w.Close(); err != nil {
		t.Error(err)
	}
	h := testHost(t, f.h.Store, f.p, &hostModel{})
	w, e := NewWebIntegration(h, f.d, f.w.Config)
	if e != nil {
		t.Fatal(e)
	}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(w.Close)
	_, messages, e = w.Conversation(t.Context(), "alice", c.ID)
	if e != nil || len(messages) != 1 {
		t.Fatal(messages, e)
	}
	inputs := messages[0].InputHistory
	if len(inputs) != 2 || inputs[1].Prompt != "奖品、总份数和每轮人数是多少？" || inputs[1].Text != second || messages[0].Run != nil || messages[0].Instruction != "" {
		t.Fatalf("missing terminal input history: %+v", messages[0])
	}
	if _, _, e = w.Conversation(t.Context(), "bob", c.ID); ErrorCode(e) != "not_found" {
		t.Fatal("another owner could read history", e)
	}
}

func TestChatCancelledBeforePublicationKeepsReadableHistory(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	c, e := f.w.CreateConversation(t.Context(), "alice", "records")
	if e != nil {
		t.Fatal(e)
	}
	m, e := f.w.SubmitMessage(t.Context(), "alice", c.ID, "cancelled", "稍后再说", "")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.w.CancelMessage(t.Context(), "alice", m.ID); e != nil {
		t.Fatal(e)
	}
	_, messages, e := f.w.Conversation(t.Context(), "alice", c.ID)
	if e != nil || len(messages) != 1 || messages[0].Status != "cancelled" || messages[0].Run != nil {
		t.Fatal(messages, e)
	}
}
