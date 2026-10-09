package service

import (
	"context"
	"fmt"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
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

func memoryProposal(r agentcontract.MemoryExtractionRequest, scope, key, value, mode string) []agentcontract.MemoryProposal {
	return []agentcontract.MemoryProposal{{Scope: scope, Key: key, Value: value, Kind: "preference", Mode: mode, Quote: r.Text}}
}

func TestMemoryChatOnlyLearnsCurrentUserMessageAndScheduledRepeatsAreOneSource(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	model := &memoryTestModel{hostModel: &hostModel{}, extract: func(r agentcontract.MemoryExtractionRequest) ([]agentcontract.MemoryProposal, error) {
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
	task, err := f.h.CreateSchedule(t.Context(), "alice", agentcontract.ScheduleRequest{Name: "report", PackID: "records", Instruction: "用中文写报告", Schedule: agentcontract.ScheduleSpec{Kind: "interval", IntervalSeconds: 1}})
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
