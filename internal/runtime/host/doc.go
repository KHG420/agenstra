// Package host owns authorized durable run lifecycles, approvals, leases, checkpoints, reconciliation, learning and schedule dispatch.
// It uses react and the run store, retaining exclusive mutable checkpoint ownership.
// Models and providers are supplied through contracts. The consumer-defined model
// selection interface freezes run configurations without importing deployment or
// model adapters. Frontends must enter through the same live authorization paths.
package host
