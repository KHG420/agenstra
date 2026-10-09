// Package web embeds the browser SDK served by the framework's HTTP endpoints.
// The JavaScript client, optional chat component and session helper are separate
// ES module exports; embedding does not add a browser runtime dependency.
package web

import "embed"

//go:embed agenstra-client.js agenstra-chat.js
var assets embed.FS

// ReadFile returns an embedded browser SDK asset relative to this package.
func ReadFile(name string) ([]byte, error) {
	return assets.ReadFile(name)
}
