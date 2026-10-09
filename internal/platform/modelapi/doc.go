// Package modelapi adapts bounded framework decisions and memory extraction to configured HTTP model protocols.
// It owns request framing, retries, cancellation, token measurement and usage
// evidence. It has no Host, registry or deployment dependency and never resolves
// application credentials or chooses a live deployment profile.
package modelapi
