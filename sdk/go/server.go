package agenstra

import (
	"time"

	"github.com/KHG420/agenstra/internal/frontend/service"
)

// HTTPServer owns HTTP routing and an optional worker lifecycle.
// The supplied host, deployment registry and run store remain caller-owned.
type HTTPServer = service.HTTPServer

// NewHTTPServer initializes required stores and starts the optional worker.
// Close the server before closing its supplied host resources.
func NewHTTPServer(host *AgentHost, deployment *Deployment, workerEnabled bool, interval time.Duration) (*HTTPServer, error) {
	return service.NewHTTPServer(host, deployment, workerEnabled, interval)
}
