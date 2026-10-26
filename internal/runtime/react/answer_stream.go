package react

import (
	"context"
	"strings"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

// Buffer partial known IDs across chunks. Business IDs are unaffected; the same
// known Fact IDs are rejected by validateFinalEvidence before publication.
func answerContext(ctx context.Context, state *agentcontract.RuntimeState) context.Context {
	parent := ctx
	return agentcontract.WithAnswerObserver(ctx, func(update agentcontract.AnswerUpdate) {
		folded := strings.ToLower(update.Text)
		end := len(update.Text)
		for _, fact := range state.Facts {
			id := strings.ToLower(fact.FactID)
			if id == "" {
				continue
			}
			if strings.Contains(folded, id) {
				agentcontract.ObserveAnswer(parent, agentcontract.AnswerUpdate{Phase: "waiting"})
				return
			}
			if update.Phase == "generating" {
				for n := min(len(id)-1, len(folded)); n > 0; n-- {
					if strings.HasSuffix(folded, id[:n]) {
						end = min(end, len(update.Text)-n)
						break
					}
				}
			}
		}
		update.Text = update.Text[:end]
		agentcontract.ObserveAnswer(parent, update)
	})
}
