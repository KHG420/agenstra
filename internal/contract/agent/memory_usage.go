package agent

import (
	"context"
)

// MeasuredMemoryExtractor is optional; existing custom extractors still work.
type MeasuredMemoryExtractor interface {
	ExtractMemoriesMeasured(context.Context, MemoryExtractionRequest) ([]MemoryProposal, ModelCallMetrics, error)
}

// MemoryInputMeasurer optionally measures the complete extraction request before
// dispatch. Implementations treat the supplied request as read-only.
type MemoryInputMeasurer interface {
	MeasureMemoryInput(MemoryExtractionRequest) (InputMeasurement, error)
}
