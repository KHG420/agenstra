package deployment

import (
	"encoding/json"
	"net/http"
	"testing"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func memoryModelReply(t *testing.T, w http.ResponseWriter, content string) {
	if callErr := json.NewEncoder(w).Encode(agentcontract.JSON{"choices": []any{agentcontract.JSON{"message": agentcontract.JSON{"content": content}}}}); callErr != nil {
		t.Error(callErr)
	}
}
