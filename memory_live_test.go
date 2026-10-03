package agenstra

import (
	"encoding/json"
	"os"
	"testing"
	"time"
)

// Explicitly opted-in classification evaluation. These synthetic inputs reach
// only the model, never a business handler or the persistent memory store.
func TestLiveMemoryTaskScopeEvaluation(t *testing.T) {
	if os.Getenv("AGENSTRA_LIVE_EVAL") != "1" {
		t.Skip("set AGENSTRA_LIVE_EVAL=1 and AGENT_MODEL credentials to run live evaluation")
	}
	model, err := NewHTTPJSONDecisionModel(os.Getenv("AGENT_MODEL"), os.Getenv("AGENT_MODEL_BASE_URL"), os.Getenv("AGENT_MODEL_API_KEY"), 60*time.Second, nil)
	if err != nil {
		t.Fatal("live evaluation requires AGENT_MODEL, AGENT_MODEL_BASE_URL and AGENT_MODEL_API_KEY")
	}
	model.APIType, model.Thinking = os.Getenv("AGENT_MODEL_API_TYPE"), os.Getenv("AGENT_MODEL_THINKING")
	model.MaxOutputTokens = 512
	records := []JSON{}
	t.Cleanup(func() {
		if path := os.Getenv("AGENSTRA_MEMORY_EVIDENCE_PATH"); path != "" {
			raw, err := json.MarshalIndent(records, "", "  ")
			if err != nil {
				t.Error(err)
				return
			}
			if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
				t.Error(err)
			}
		}
	})
	for _, tc := range []struct {
		name, text string
		lasting    bool
	}{
		{"cancel_read_only", "取消刚才的修改要求。不要修改任何用户，只重新读取 Page5 的当前资料，用表格回答。", false},
		{"missing_parameters", "创建名为 Synthetic-Group 的分组。缺少必要参数时请向我追问，不要自行猜测。", false},
		{"english_task", "For this task, read record R-1 without modifying any records. Cancel the previous update request.", false},
		{"temporary_format", "这次报告用英文，以后仍按原来的设置。", false},
		{"lasting_rule", "以后这个项目缺少必填参数时，一律先问我，不能自行猜测。", true},
		{"lasting_language", "以后所有报告都用中文，记住这个偏好。", true},
	} {
		for attempt := 0; attempt < 2; attempt++ {
			t.Run(tc.name+string(rune('1'+attempt)), func(t *testing.T) {
				proposals, metrics, err := model.ExtractMemoriesMeasured(t.Context(), MemoryExtractionRequest{Text: tc.text, Existing: []MemoryView{}})
				lasting := 0
				for _, p := range proposals {
					if p.Mode == "explicit" || p.Mode == "habit" {
						lasting++
					}
				}
				pass := err == nil && (lasting > 0) == tc.lasting
				records = append(records, JSON{"case": tc.name, "attempt": attempt + 1, "text": tc.text, "expected_lasting": tc.lasting, "pass": pass, "proposals": proposals, "model_call": metrics, "error_code": ErrorCode(err)})
				t.Logf("case=%s pass=%t requests=%d input_tokens=%d output_tokens=%d proposals=%+v", tc.name, pass, metrics.Attempts, metrics.InputTokens, metrics.OutputTokens, proposals)
				if !pass {
					t.Errorf("task scope classification: lasting=%d expected=%t error_code=%s", lasting, tc.lasting, ErrorCode(err))
				}
			})
		}
	}
}
