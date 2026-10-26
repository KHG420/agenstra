package modelapi

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

// SSE is transport only: reassemble the original envelope and pass it through
// the same usage, refusal, truncation and strict decision checks as non-streaming.
func readModelStream(reader io.Reader, publish func(string, string), allowedOutput map[string]bool) ([]byte, error) {
	const maximum = 8 << 20
	bounded := &io.LimitedReader{R: reader, N: maximum + 1}
	scanner := bufio.NewScanner(bounded)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var content, reasoning, refusal, model, finish string
	var usage json.RawMessage
	tools := map[int]*decisionOutputToolCall{}
	var data []string
	done, sawChoice := false, false
	var last time.Time
	displayed := ""
	preview := func(force bool) {
		if !force && time.Since(last) < 40*time.Millisecond {
			return
		}
		last = time.Now()
		text := ""
		if refusal == "" && finish != "length" && finish != "content_filter" {
			if len(tools) == 1 && tools[0] != nil && allowedOutput[tools[0].Function.Name] && strings.TrimSpace(content) == "" {
				switch tools[0].Function.Name {
				case "submit_final", "final":
					text = answerPrefix(tools[0].Function.Arguments, false)
				case "submit_decision":
					text = answerPrefix(tools[0].Function.Arguments, true)
				}
			} else if len(tools) == 0 && len(allowedOutput) == 0 {
				text = answerPrefix(content, true)
			}
		}
		if len(text) > 120000 {
			text = ""
		}
		if text != "" {
			phase := "generating"
			if force {
				phase = "checking"
			}
			publish(text, phase)
		} else if displayed != "" {
			publish("", "waiting")
		}
		displayed = text
	}
	invalid := func() error { return agentcontract.ModelDecisionError{Kind: "model_response_invalid"} }
	consume := func() error {
		if len(data) == 0 {
			return nil
		}
		raw := strings.Join(data, "\n")
		data = nil
		if raw == "[DONE]" {
			done = true
			return nil
		}
		var event struct {
			Model   string          `json:"model"`
			Usage   json.RawMessage `json:"usage"`
			Error   json.RawMessage `json:"error"`
			Choices []struct {
				Index  *int   `json:"index"`
				Finish string `json:"finish_reason"`
				Delta  struct {
					Content   string `json:"content"`
					Reasoning string `json:"reasoning_content"`
					Refusal   string `json:"refusal"`
					Tools     []struct {
						Index *int `json:"index"`
						decisionOutputToolCall
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if modelJSONFormatError([]byte(raw)) != "" || json.Unmarshal([]byte(raw), &event) != nil || (len(event.Error) > 0 && string(event.Error) != "null") || len(event.Choices) > 1 {
			return invalid()
		}
		if event.Model != "" {
			model = event.Model
		}
		if len(event.Usage) > 0 && string(event.Usage) != "null" {
			usage = event.Usage
		}
		for _, choice := range event.Choices {
			if choice.Index == nil || *choice.Index != 0 || finish != "" {
				return invalid()
			}
			sawChoice = true
			content += choice.Delta.Content
			reasoning += choice.Delta.Reasoning
			refusal += choice.Delta.Refusal
			finish = choice.Finish
			for _, delta := range choice.Delta.Tools {
				if delta.Index == nil || *delta.Index < 0 || *delta.Index > 31 {
					return invalid()
				}
				tool := tools[*delta.Index]
				if tool == nil {
					tool = &decisionOutputToolCall{}
					tools[*delta.Index] = tool
				}
				tool.Type += delta.Type
				tool.Function.Name += delta.Function.Name
				tool.Function.Arguments += delta.Function.Arguments
			}
		}
		preview(false)
		return nil
	}
	for scanner.Scan() {
		if bounded.N == 0 {
			return nil, agentcontract.ModelDecisionError{Kind: "model_response_too_large"}
		}
		line := scanner.Text()
		if line == "" {
			if err := consume(); err != nil {
				return nil, err
			}
			if done {
				break
			}
		} else if strings.HasPrefix(line, "data:") {
			data = append(data, strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " "))
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return nil, agentcontract.ModelDecisionError{Kind: "model_response_too_large"}
		}
		return nil, err
	}
	if err := consume(); err != nil {
		return nil, err
	}
	if !done || !sawChoice || finish == "" {
		return nil, io.ErrUnexpectedEOF
	}
	calls := make([]*decisionOutputToolCall, len(tools))
	for i := range calls {
		if tools[i] == nil {
			return nil, invalid()
		}
		calls[i] = tools[i]
	}
	preview(true)
	return json.Marshal(agentcontract.JSON{"model": model, "usage": usage, "choices": []any{agentcontract.JSON{"finish_reason": finish, "message": agentcontract.JSON{"content": content, "reasoning_content": reasoning, "refusal": refusal, "tool_calls": calls}}}})
}
