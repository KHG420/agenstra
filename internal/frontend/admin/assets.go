// Package admin owns the developer console's embedded static assets.
// Authentication and management operations remain in the execution engine.
package admin

import "embed"

//go:embed admin_ui.html admin_ui.css admin_ui.js admin_drafts.js admin_models.js
var assets embed.FS

// ReadFile returns an embedded console asset relative to this package.
func ReadFile(name string) ([]byte, error) {
	return assets.ReadFile(name)
}
