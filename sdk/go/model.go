package agenstra

import (
	"context"
	"net/http"
	"time"

	deployassembly "github.com/KHG420/agenstra/internal/assembly/deployment"
	"github.com/KHG420/agenstra/internal/contract/agent"
	"github.com/KHG420/agenstra/internal/platform/modelapi"
)

// ModelDecisionError carries a safe model transport or format error code.
type ModelDecisionError = agent.ModelDecisionError

// HTTPJSONDecisionModel adapts a compatible JSON chat endpoint to typed decisions.
// Configure it before concurrent use; supplied HTTP clients remain caller-owned.
type HTTPJSONDecisionModel = modelapi.HTTPJSONDecisionModel

// NewHTTPJSONDecisionModel configures a bounded model adapter without contacting the service.
func NewHTTPJSONDecisionModel(model, baseURL, apiKey string, timeout time.Duration, client *http.Client) (*HTTPJSONDecisionModel, error) {
	return modelapi.NewHTTPJSONDecisionModel(model, baseURL, apiKey, timeout, client)
}

// ModelInfoProvider is optional. Unknown capacities stay nil; no model-name guesses.
type ModelInfoProvider = agent.ModelInfoProvider

// ModelInfo describes known model capacities; nil limits mean unknown capacity.
type ModelInfo = agent.ModelInfo

// InputMeasurement records input token measurement and projection evidence.
type InputMeasurement = agent.InputMeasurement

// ModelInputMeasurer optionally measures the same packet and prompt sent to Decide.
// Implementations treat the packet as read-only.
// Unknown uncoded failures become model_unavailable; a coded error exposes its deliberate safe code.
type ModelInputMeasurer = agent.ModelInputMeasurer

// ModelRequestProgress reports transport attempts and retry timing without upstream response text.
type ModelRequestProgress = agent.ModelRequestProgress

// WithModelRequestObserver lets embedded hosts observe retries without replacing
// DecisionModel. Callback failure prevents the next transport attempt.
func WithModelRequestObserver(ctx context.Context, observer func(ModelRequestProgress) error) context.Context {
	return agent.WithModelRequestObserver(ctx, observer)
}

// ModelOutput limits which fields of a provider result may enter model context
// or be read through a Fact reference. Paths are relative to the result data.
// A nil ModelOutput keeps the existing full-result behavior; an empty Paths
// list makes the result opaque to the model.
type ModelOutput = agent.ModelOutput

// ModelConfiguration contains connection references, never resolved API keys.
// Empty purpose selections inherit DefaultProfile.
type ModelConfiguration = agent.ModelConfiguration

// ModelProfile configures one model adapter using connection references rather than resolved secrets.
type ModelProfile = agent.ModelProfile

// ModelPrices holds optional per-million-token prices used only for cost estimates.
type ModelPrices = agent.ModelPrices

// ModelSelectionSnapshot binds a configuration to a management revision.
type ModelSelectionSnapshot = agent.ModelSelectionSnapshot

// ModelManager owns deployment selection. Runs retain a connection-reference
// snapshot, so editing the catalog cannot reroute an in-progress task.
type ModelManager = deployassembly.ModelManager

// ModelCheckResult records the outcome and usage of one synthetic profile probe.
type ModelCheckResult = agent.ModelCheckResult

// ModelCallMetrics retains request evidence even when a model call fails.
// UsageAvailable distinguishes provider-reported usage from an estimate.
// EstimatedCostUSD is populated only when the caller configured model prices.
type ModelCallMetrics = agent.ModelCallMetrics

// ModelUsage aggregates reported and estimated token use across model requests and purposes.
type ModelUsage = agent.ModelUsage
