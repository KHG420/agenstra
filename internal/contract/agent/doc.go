// Package agent defines the shared value contracts, validation and decision protocol used by framework layers.
// It has no Host, database, deployment, credential resolution or transport ownership.
// Values are aliased by the public Go SDK; drivers own mutable runtime state,
// while model and HTTP projections are detached views with their existing wire schemas.
package agent
