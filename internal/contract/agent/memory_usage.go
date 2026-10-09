package agent

import (
	"context"
)

// MeasuredMemoryExtractor is optional; existing custom extractors still work.
type MeasuredMemoryExtractor interface {
	ExtractMemoriesMeasured(context.Context, MemoryExtractionRequest) ([]MemoryProposal, ModelCallMetrics, error)
}
