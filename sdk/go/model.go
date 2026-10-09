package agenstra

import (
	"context"
	"net/http"
	"time"

	"github.com/KHG420/agenstra/internal/runtime/engine"
)

// ModelDecisionError carries a safe model transport or format error code.
type ModelDecisionError = engine.ModelDecisionError

// HTTPJSONDecisionModel adapts a compatible JSON chat endpoint to typed decisions.
// Configure it before concurrent use; supplied HTTP clients remain caller-owned.
type HTTPJSONDecisionModel = engine.HTTPJSONDecisionModel

// NewHTTPJSONDecisionModel configures a bounded model adapter without contacting the service.
func NewHTTPJSONDecisionModel(model, baseURL, apiKey string, timeout time.Duration, client *http.Client) (*HTTPJSONDecisionModel, error) {
	return engine.NewHTTPJSONDecisionModel(model, baseURL, apiKey, timeout, client)
}

// ModelInfoProvider is optional. Unknown capacities stay nil; no model-name guesses.
type ModelInfoProvider = engine.ModelInfoProvider

// ModelInfo describes known model capacities; nil limits mean unknown capacity.
type ModelInfo = engine.ModelInfo

// InputMeasurement records input token measurement and projection evidence.
type InputMeasurement = engine.InputMeasurement

// ModelInputMeasurer optionally measures the same packet and prompt sent to Decide.
// Implementations treat the packet as read-only.
// Unknown uncoded failures become model_unavailable; a coded error exposes its deliberate safe code.
type ModelInputMeasurer = engine.ModelInputMeasurer

// ModelRequestProgress reports transport attempts and retry timing without upstream response text.
type ModelRequestProgress = engine.ModelRequestProgress

// WithModelRequestObserver lets embedded hosts observe retries without replacing
// DecisionModel. Callback failure prevents the next transport attempt.
func WithModelRequestObserver(ctx context.Context, observer func(ModelRequestProgress) error) context.Context {
	return engine.WithModelRequestObserver(ctx, observer)
}

// ModelOutput limits which fields of a provider result may enter model context
// or be read through a Fact reference. Paths are relative to the result data.
// A nil ModelOutput keeps the existing full-result behavior; an empty Paths
// list makes the result opaque to the model.
type ModelOutput = engine.ModelOutput

// ModelConfiguration contains connection references, never resolved API keys.
// Empty purpose selections inherit DefaultProfile.
type ModelConfiguration = engine.ModelConfiguration

// ModelProfile configures one model adapter using connection references rather than resolved secrets.
type ModelProfile = engine.ModelProfile

// ModelPrices holds optional per-million-token prices used only for cost estimates.
type ModelPrices = engine.ModelPrices

// ModelSelectionSnapshot binds a configuration to a management revision.
type ModelSelectionSnapshot = engine.ModelSelectionSnapshot

// ModelManager owns deployment selection. Runs retain a connection-reference
// snapshot, so editing the catalog cannot reroute an in-progress task.
type ModelManager = engine.ModelManager

// ModelCheckResult records the outcome and usage of one synthetic profile probe.
type ModelCheckResult = engine.ModelCheckResult

// ModelCallMetrics retains request evidence even when a model call fails.
// UsageAvailable distinguishes provider-reported usage from an estimate.
// EstimatedCostUSD is populated only when the caller configured model prices.
type ModelCallMetrics = engine.ModelCallMetrics

// ModelUsage aggregates reported and estimated token use across model requests and purposes.
type ModelUsage = engine.ModelUsage
