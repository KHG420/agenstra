// Package mcptransport owns MCP JSON-RPC connections without host state.
package mcptransport

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"

	"github.com/KHG420/agenstra/internal/jsonvalue"
)

// Stdio owns a started subprocess and serializes its JSON-RPC exchanges.
// Close it after outstanding requests have stopped.
type Stdio struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    *bufio.Reader
	mu        chan struct{}
	muOnce    sync.Once
	nextID    int
	closeOnce sync.Once
	closeErr  error
}

func (c *Stdio) request(ctx context.Context, method string, params map[string]any, notification bool) (map[string]any, error) {
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
	raw = append(raw, '\n')
	written := make(chan error, 1)
	go func() {
		_, err := c.stdin.Write(raw)
		written <- err
	}()
	select {
	case <-ctx.Done():
		closeErr := c.Close()
		writeErr := <-written
		return nil, errors.Join(ctx.Err(), closeErr, writeErr)
	case err := <-written:
		if err != nil {
			return nil, err
		}
	}
	if notification {
		return nil, nil
	}
	type readResult struct {
		line []byte
		err  error
	}
	for {
		read := make(chan readResult, 1)
		go func() { line, err := readStdioFrame(c.stdout, 16<<20); read <- readResult{line, err} }()
		var got readResult
		select {
		case <-ctx.Done():
			closeErr := c.Close()
			got = <-read
			return nil, errors.Join(ctx.Err(), closeErr, got.err)
		case got = <-read:
		}
		line, err := got.line, got.err
		if err != nil {
			// An incomplete or oversized frame cannot be reused by another request.
			return nil, errors.Join(err, c.Close())
		}
		var reply map[string]any
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.UseNumber()
		if !json.Valid(line) || dec.Decode(&reply) != nil {
			return nil, errors.New("invalid MCP stdio response")
		}
		_, hasMethod := reply["method"]
		if hasMethod || reply["id"] == nil {
			continue
		}
		id, ok := jsonvalue.Index(reply["id"])
		if !ok || id != c.nextID {
			continue
		}
		_, hasResult := reply["result"]
		_, hasError := reply["error"]
		if reply["jsonrpc"] != "2.0" || hasResult == hasError {
			return nil, errors.New("invalid MCP response envelope")
		}
		if reply["error"] != nil {
			return nil, fmt.Errorf("MCP error: %v", reply["error"])
		}
		result, ok := reply["result"].(map[string]any)
		if !ok {
			return nil, errors.New("invalid MCP result")
		}
		return result, nil
	}
}

func readStdioFrame(reader *bufio.Reader, limit int) ([]byte, error) {
	var line []byte
	for {
		part, err := reader.ReadSlice('\n')
		if len(part) > limit-len(line) {
			return nil, errors.New("MCP stdio response too large")
		}
		line = append(line, part...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		return line, err
	}
}

// Request sends one JSON-RPC request and waits for its matching object result.
func (c *Stdio) Request(ctx context.Context, m string, p map[string]any) (map[string]any, error) {
	return c.request(ctx, m, p, false)
}

// Notify sends a JSON-RPC notification without waiting for a response.
func (c *Stdio) Notify(ctx context.Context, m string, p map[string]any) error {
	_, e := c.request(ctx, m, p, true)
	return e
}

// Close releases owned connection resources after outstanding calls have stopped.
func (c *Stdio) Close() error {
	c.closeOnce.Do(func() {
		stdinErr := c.stdin.Close()
		if errors.Is(stdinErr, os.ErrClosed) {
			stdinErr = nil
		}
		var killErr error
		if c.cmd.Process != nil {
			killErr = c.cmd.Process.Kill()
			if errors.Is(killErr, os.ErrProcessDone) {
				killErr = nil
			}
		}
		waitErr := c.cmd.Wait()
		if _, ok := waitErr.(*exec.ExitError); ok {
			// Closing a stdio connection intentionally terminates its subprocess.
			waitErr = nil
		}
		c.closeErr = errors.Join(stdinErr, killErr, waitErr)
	})
	return c.closeErr
}

// StartStdio starts a caller-configured command and takes ownership of its pipes and process.
// The command's context controls the connection lifetime; request deadlines are separate.
func StartStdio(cmd *exec.Cmd) (*Stdio, error) {
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, errors.Join(err, stdin.Close())
	}
	if err := cmd.Start(); err != nil {
		return nil, errors.Join(err, stdin.Close(), stdout.Close())
	}
	return &Stdio{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}, nil
}
