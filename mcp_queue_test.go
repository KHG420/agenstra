package agenstra

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMCPHTTPQueuedCancellationPreservesTheActiveRequest(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request JSON
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			return
		}
		if requests.Add(1) == 1 {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(JSON{"jsonrpc": "2.0", "id": request["id"], "result": JSON{"capacity": 2400}})
	}))
	defer server.Close()
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	client := &httpMCP{url: server.URL, client: server.Client()}
	firstCtx, cancelFirst := context.WithCancel(t.Context())
	defer cancelFirst()
	firstDone := make(chan error, 1)
	go func() {
		_, err := client.Request(firstCtx, "tools/call", JSON{})
		firstDone <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first request did not reach the server")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	secondDone := make(chan error, 1)
	go func() {
		_, err := client.Request(ctx, "tools/call", JSON{})
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("queued request did not return its deadline: %v", err)
		}
	case <-time.After(2 * time.Second):
		releaseOnce.Do(func() { close(release) })
		<-firstDone
		<-secondDone
		t.Fatal("queued request ignored its context deadline")
	}
	select {
	case err := <-firstDone:
		t.Fatalf("queued cancellation interrupted the active request: %v", err)
	default:
	}
	if requests.Load() != 1 {
		t.Fatalf("cancelled queued operation reached the server: %d", requests.Load())
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-firstDone; err != nil {
		t.Fatalf("active request failed: %v", err)
	}
	if _, err := client.Request(t.Context(), "tools/call", JSON{}); err != nil || requests.Load() != 2 {
		t.Fatalf("connection did not remain usable: %v requests=%d", err, requests.Load())
	}
}

type mcpQueueSignalWriter struct {
	io.WriteCloser
	started chan struct{}
	once    sync.Once
}

func (w *mcpQueueSignalWriter) Write(raw []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	return w.WriteCloser.Write(raw)
}

func TestMCPStdioQueuedCancellationPreservesTheActiveProcess(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestMCPStdioCancellationInterruptsBlockedWrite$")
	cmd.Env = append(os.Environ(), "AGENSTRA_MCP_BLOCKED_STDIN_HELPER=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	client := &stdioMCP{cmd: cmd, stdin: &mcpQueueSignalWriter{WriteCloser: stdin, started: started}, stdout: bufio.NewReader(stdout)}
	t.Cleanup(func() {
		_ = client.Close()
		if cmd.ProcessState == nil {
			t.Error("test subprocess was not reaped")
		}
	})
	if line, err := client.stdout.ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("helper failed to start: %q %v", line, err)
	}
	firstCtx, cancelFirst := context.WithCancel(t.Context())
	firstDone := make(chan error, 1)
	go func() {
		_, err := client.Request(firstCtx, "tools/call", JSON{"payload": strings.Repeat("x", 1<<20)})
		firstDone <- err
	}()
	defer func() { cancelFirst(); <-firstDone }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("first request did not start writing")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	secondDone := make(chan error, 1)
	go func() {
		_, err := client.Request(ctx, "tools/call", JSON{})
		secondDone <- err
	}()
	select {
	case err := <-secondDone:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("queued request did not return its deadline: %v", err)
		}
	case <-time.After(2 * time.Second):
		cancelFirst()
		<-secondDone
		t.Fatal("queued request ignored its context deadline")
	}
	if cmd.ProcessState != nil {
		t.Fatal("queued cancellation terminated the active subprocess")
	}
}
