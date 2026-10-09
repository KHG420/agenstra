package agent

import (
	"context"
)

// ReconciliationContext gives a verifier a detached copy of the original uncertain invocation and trusted identity.
type ReconciliationContext struct {
	RunID        string
	OwnerID      string
	OriginPackID string
	Invocation   Invocation
	Identity     InvocationContext
	Capability   CapabilityDescription
}

// InvocationReconciler must verify the original invocation using authoritative
// business evidence (for example its idempotency key). It must not replay it.
// Return only a settled result; a verification failure leaves the run paused.
type InvocationReconciler func(context.Context, ReconciliationContext) (CapabilityResult, error)
