// Package service owns HTTP routing, the optional joined worker lifecycle, chat integration and browser command coordination.
// It delegates authorized execution to Host and deployment, and owns only optional
// web integration tables. Browser generations, acknowledgements and reconciliation
// retain the original invocation identity. Administrative assets and Web SDK
// assets are embedded from their owning packages. Close joins workers before
// releasing integration resources; supplied host stores remain caller-owned.
package service
