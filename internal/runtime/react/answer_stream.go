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
		text := update.Text
		preview := update.ReasoningPreview != ""
		if preview {
			text = update.ReasoningPreview
		}
		folded := strings.ToLower(text)
		end := len(text)
		for _, fact := range state.Facts {
			id := strings.ToLower(fact.FactID)
			if id == "" {
				continue
			}
			if strings.Contains(folded, id) {
				agentcontract.ObserveAnswer(parent, agentcontract.AnswerUpdate{Phase: "waiting"})
				return
			}
			if update.Phase == "generating" || preview {
				for n := min(len(id)-1, len(folded)); n > 0; n-- {
					if strings.HasSuffix(folded, id[:n]) {
						end = min(end, len(text)-n)
						break
					}
				}
			}
		}
		if preview {
			update.ReasoningPreview = text[:end]
		} else {
			update.Text = text[:end]
		}
		agentcontract.ObserveAnswer(parent, update)
	})
}
