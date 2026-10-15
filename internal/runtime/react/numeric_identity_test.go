package react

import (
	"encoding/json"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestArgumentsDigestDistinguishesExactDecimalNumbers(t *testing.T) {
	for _, pair := range [][2]string{{"9007199254740992.0", "9007199254740993.0"}, {"9223372036854775806.0", "9223372036854775807.0"}, {"0.1", "0.100000000000000000001"}, {"0.0", "1e-400"}} {
		left := ArgumentsDigest(agentcontract.ToolCall{Capability: "record.update", Arguments: agentcontract.JSON{"value": json.Number(pair[0])}})
		right := ArgumentsDigest(agentcontract.ToolCall{Capability: "record.update", Arguments: agentcontract.JSON{"value": json.Number(pair[1])}})
		if left == "" || right == "" || left == right {
			t.Fatalf("different exact arguments share approval identity: %s / %s", pair[0], pair[1])
		}
	}
}

func TestArgumentsDigestNormalizesEquivalentDecimalSpellings(t *testing.T) {
	for _, pair := range [][2]string{{"9007199254740993.0", "90071992547409930e-1"}, {"0.100000000000000000001", "1.00000000000000000001e-1"}, {"1e-400", "10e-401"}} {
		left := ArgumentsDigest(agentcontract.ToolCall{Capability: "record.update", Arguments: agentcontract.JSON{"value": json.Number(pair[0])}})
		right := ArgumentsDigest(agentcontract.ToolCall{Capability: "record.update", Arguments: agentcontract.JSON{"value": json.Number(pair[1])}})
		if left == "" || left != right {
			t.Fatalf("equivalent decimal spellings changed identity: %s / %s", pair[0], pair[1])
		}
	}
}
