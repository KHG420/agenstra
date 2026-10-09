// Package registry owns immutable capability releases, activation, bindings, drafts and model-configuration revisions in SQLite.
// It validates package files through capability contracts and keeps publication,
// revision checks and audit writes atomic. It owns its database connection and
// exposes operations rather than lending its private database to assembly code.
package registry
