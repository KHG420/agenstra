// Package deployment wires trusted deployment configuration, identity verification, connection references, model selections and business callbacks.
// Host owns execution and the registry owns persistent management transactions.
// ModelManager satisfies the Host selection interface, so assembly depends on
// the runtime rather than the runtime importing its concrete configuration owner.
package deployment
