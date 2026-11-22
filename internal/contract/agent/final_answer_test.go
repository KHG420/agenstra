package agent

import (
	"strings"
	"testing"
)

func TestFinalAnswerSchemaRecovery(t *testing.T) {
	s, err := CompileFinalAnswerSchema(JSON{"type": "object", "required": []string{"rows"}, "additionalProperties": false, "properties": JSON{"rows": JSON{"type": "array"}}})
	if err != nil {
		t.Fatal(err)
	}
	object := `{"rows":[{"id":"x\"}]","number":9007199254740993}]}`
	for _, input := range []string{
		object, strings.TrimSuffix(object, "]}") + ",],}", "Here is the result:\n```JSON\r\n" + object + "\r\n```\n以上为结果。",
		"结果如下：" + object + " 调查完成。",
		"```json\n" + strings.TrimSuffix(object, "]}") + "\n```",
	} {
		got, err := s.Normalize(input)
		if err != nil || got != object {
			t.Fatalf("recovery changed values: %q, %v", got, err)
		}
	}
	for _, input := range []string{
		object + " {}", object + " null", "null " + object, "[" + object + "]", "```python\n" + object + "\n```",
		"```json\n" + object + "\n```\n```json\n" + object + "\n```",
		`{"rows":[123`, `{"rows":["partial`, "{\"rows\":[\"escape\\", `{"rows":[`, `{"rows":`,
		`{"rows":[},"x"]}`, `{"rows":[],"rows":[]}`, `{"rows":[{"id":1,"\u0069d":2}]}`,
		`{"rows":[],"note":"extra"}`, `{"rows":"wrong"}`, `{}`,
	} {
		if got, err := s.Normalize(input); err == nil {
			t.Errorf("accepted ambiguous or invalid output %q: %q", input, got)
		}
	}
}

func TestFinalAnswerArrayAndSchemaLimits(t *testing.T) {
	s, err := CompileFinalAnswerSchema(JSON{"type": "array"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Normalize("Result:\n[true, null")
	if err != nil || got != "[true, null]" {
		t.Fatalf("%q %v", got, err)
	}
	for _, schema := range []JSON{nil, {"$ref": "https://example.invalid/schema"}, {"description": strings.Repeat("a", 16<<10)}} {
		if _, err := CompileFinalAnswerSchema(schema); err == nil {
			t.Fatal("invalid deployment contract accepted")
		}
	}
}
