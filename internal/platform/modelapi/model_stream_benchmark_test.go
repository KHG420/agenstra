package modelapi

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// Keep the output fixed while varying upstream fragmentation. Small token
// deltas must not require copying the complete answer and reasoning each time.
func fragmentedModelStream(tb testing.TB, fragments int, tool bool) string {
	tb.Helper()
	var stream strings.Builder
	chunk := strings.Repeat("a", 65536/fragments)
	for i := range fragments {
		delta := map[string]any{"reasoning_content": chunk}
		if tool {
			call := map[string]any{"index": 0, "function": map[string]any{"arguments": chunk}}
			if i == 0 {
				call["type"] = "function"
				call["function"].(map[string]any)["name"] = "record_read"
			}
			delta["tool_calls"] = []any{call}
		} else {
			delta["content"] = chunk
		}
		raw, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "delta": delta}}})
		if err != nil {
			tb.Fatal(err)
		}
		stream.WriteString("data: ")
		stream.Write(raw)
		stream.WriteString("\n\n")
	}
	stream.WriteString("data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	return stream.String()
}

func BenchmarkFragmentedModelStream(b *testing.B) {
	for _, tool := range []bool{false, true} {
		for _, fragments := range []int{128, 4096} {
			b.Run(fmt.Sprintf("tool=%t/fragments=%d", tool, fragments), func(b *testing.B) {
				stream := fragmentedModelStream(b, fragments, tool)
				b.ReportAllocs()
				b.SetBytes(int64(len(stream)))
				b.ResetTimer()
				for range b.N {
					if _, err := readModelStream(strings.NewReader(stream), func(string, string) {}, map[string]bool{"record_read": true}, false); err != nil {
						b.Fatal(err)
					}
				}
			})
		}
	}
}

func TestFragmentedModelStreamRetainsCompleteFields(t *testing.T) {
	for _, tool := range []bool{false, true} {
		raw, err := readModelStream(strings.NewReader(fragmentedModelStream(t, 4096, tool)), func(string, string) {}, map[string]bool{"record_read": true}, false)
		if err != nil {
			t.Fatal(err)
		}
		var envelope struct {
			Choices []struct {
				Message struct {
					Content   string                   `json:"content"`
					Reasoning string                   `json:"reasoning_content"`
					Tools     []decisionOutputToolCall `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(raw, &envelope); err != nil || len(envelope.Choices) != 1 {
			t.Fatal("fragmented envelope was not reconstructed", err)
		}
		message := envelope.Choices[0].Message
		want := strings.Repeat("a", 65536)
		if message.Reasoning != want {
			t.Fatal("reasoning fragments were lost")
		}
		if tool {
			if message.Content != "" || len(message.Tools) != 1 || message.Tools[0].Function.Name != "record_read" || message.Tools[0].Type != "function" || message.Tools[0].Function.Arguments != want {
				t.Fatal("tool fragments were lost or changed")
			}
		} else if message.Content != want || len(message.Tools) != 0 {
			t.Fatal("content fragments were lost or changed")
		}
	}
}
