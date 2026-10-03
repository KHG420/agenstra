package mcptransport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/KHG420/agenstra/internal/jsonvalue"
)

// HTTP serializes JSON-RPC exchanges and owns the negotiated MCP session.
// Close it after outstanding requests have stopped.
type HTTP struct {
	url       string
	token     string
	client    *http.Client
	session   string
	protocol  string
	mu        chan struct{}
	muOnce    sync.Once
	nextID    int
	closeOnce sync.Once
	closeErr  error
}

func (c *HTTP) request(ctx context.Context, method string, params map[string]any, notification bool) (map[string]any, error) {
	c.muOnce.Do(func() { c.mu = make(chan struct{}, 1) })
	select {
	case c.mu <- struct{}{}:
		defer func() { <-c.mu }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	msg := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
	if !notification {
		c.nextID++
		msg["id"] = c.nextID
	}
	raw, err := jsonvalue.Canonical(msg)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", c.url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	version := c.protocol
	if version == "" {
		version = "2025-03-26"
	}
	req.Header.Set("Mcp-Protocol-Version", version)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}
	res, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := res.Body.Close(); err != nil {
			log.Print("HTTP response cleanup failed")
		}
	}()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("MCP HTTP %d", res.StatusCode)
	}
	if session := res.Header.Get("Mcp-Session-Id"); session != "" {
		c.session = session
	}
	if notification {
		return nil, nil
	}
	var reply map[string]any
	mediaType, _, err := mime.ParseMediaType(res.Header.Get("Content-Type"))
	if err != nil {
		return nil, errors.New("invalid MCP response content type")
	}
	if mediaType == "text/event-stream" {
		scanner := bufio.NewScanner(io.LimitReader(res.Body, 16<<20))
		scanner.Buffer(make([]byte, 4096), 16<<20)
		scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
			if i := bytes.IndexAny(data, "\r\n"); i >= 0 {
				if data[i] == '\r' && i+1 == len(data) && !atEOF {
					return 0, nil, nil
				}
				advance := i + 1
				if data[i] == '\r' && advance < len(data) && data[advance] == '\n' {
					advance++
				}
				return advance, data[:i], nil
			}
			if atEOF && len(data) > 0 {
				return len(data), data, nil
			}
			return 0, nil, nil
		})
		var eventData strings.Builder
		firstLine := true
		for scanner.Scan() {
			line := scanner.Text()
			if firstLine {
				line = strings.TrimPrefix(line, "\ufeff")
				firstLine = false
			}
			if line == "data" || strings.HasPrefix(line, "data:") {
				payload := strings.TrimPrefix(strings.TrimPrefix(line, "data"), ":")
				eventData.WriteString(strings.TrimPrefix(payload, " "))
				eventData.WriteByte('\n')
				continue
			}
			if line != "" || eventData.Len() == 0 {
				continue
			}
			payload := eventData.String()
			eventData.Reset()
			if strings.TrimSpace(payload) == "" {
				continue
			}
			if !json.Valid([]byte(payload)) {
				return nil, errors.New("invalid MCP SSE event")
			}
			var event map[string]any
			dec := json.NewDecoder(strings.NewReader(payload))
			dec.UseNumber()
			if err := dec.Decode(&event); err != nil {
				return nil, err
			}
			id, ok := jsonvalue.Index(event["id"])
			if event["method"] != nil || !ok || id != c.nextID {
				continue
			}
			reply = event
			break
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	} else {
		raw, err := io.ReadAll(io.LimitReader(res.Body, (16<<20)+1))
		if err != nil {
			return nil, err
		}
		if len(raw) > 16<<20 || !json.Valid(raw) {
			return nil, errors.New("invalid MCP JSON response")
		}
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.UseNumber()
		if err := dec.Decode(&reply); err != nil {
			return nil, err
		}
	}
	_, hasResult := reply["result"]
	_, hasError := reply["error"]
	_, hasMethod := reply["method"]
	if reply["jsonrpc"] != "2.0" || hasMethod || hasResult == hasError {
		return nil, errors.New("invalid MCP response envelope")
	}
	if reply["error"] != nil {
		return nil, fmt.Errorf("MCP error: %v", reply["error"])
	}
	id, ok := jsonvalue.Index(reply["id"])
	if !ok || id != c.nextID {
		return nil, errors.New("invalid MCP response id")
	}
	result, ok := reply["result"].(map[string]any)
	if !ok {
		return nil, errors.New("invalid MCP result")
	}
	return result, nil
}

// Request sends one JSON-RPC request and waits for its matching object result.
func (c *HTTP) Request(ctx context.Context, m string, p map[string]any) (map[string]any, error) {
	return c.request(ctx, m, p, false)
}

// Notify sends a JSON-RPC notification without waiting for a response.
func (c *HTTP) Notify(ctx context.Context, m string, p map[string]any) error {
	_, e := c.request(ctx, m, p, true)
	return e
}

// Close releases owned connection resources after outstanding calls have stopped.
func (c *HTTP) Close() error {
	c.closeOnce.Do(func() {
		if c.session == "" {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.url, nil)
		if err != nil {
			c.closeErr = err
			return
		}
		req.Header.Set("Mcp-Session-Id", c.session)
		version := c.protocol
		if version == "" {
			version = "2025-03-26"
		}
		req.Header.Set("Mcp-Protocol-Version", version)
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		res, err := c.client.Do(req)
		if err != nil {
			c.closeErr = err
			return
		}
		c.closeErr = res.Body.Close()
		if res.StatusCode != http.StatusOK && res.StatusCode != http.StatusNoContent && res.StatusCode != http.StatusNotFound {
			c.closeErr = errors.Join(c.closeErr, fmt.Errorf("MCP session close: HTTP %d", res.StatusCode))
		}
	})
	return c.closeErr
}

// NewHTTP opens a session transport without changing the supplied HTTP client.
// The caller selects the client's timeout and redirect policy before use.
func NewHTTP(url, token string, client *http.Client) *HTTP {
	return &HTTP{url: url, token: token, client: client}
}

// SetProtocolVersion records the version returned by initialization.
// Call it before subsequent requests, while no request is active.
func (c *HTTP) SetProtocolVersion(version string) {
	c.protocol = version
}
