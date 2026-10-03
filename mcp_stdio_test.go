package agenstra

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestMCPStdioCancellationInterruptsBlockedWrite(t *testing.T) {
	if os.Getenv("AGENSTRA_MCP_BLOCKED_STDIN_HELPER") == "1" {
		_, _ = fmt.Fprintln(os.Stdout, "ready")
		time.Sleep(30 * time.Second)
		return
	}
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
	client := &stdioMCP{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}
	defer client.Close()
	if line, err := client.stdout.ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("helper failed to start: %q %v", line, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := client.Request(ctx, "tools/call", JSON{"payload": strings.Repeat("x", 1<<20)})
		finished <- err
	}()
	select {
	case err := <-finished:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("deadline was not returned: %v", err)
		}
		if cmd.ProcessState == nil {
			t.Fatal("cancelled subprocess was not reaped")
		}
	case <-time.After(2 * time.Second):
		_ = client.Close()
		<-finished
		t.Fatal("context deadline did not interrupt a blocked stdin write")
	}
}

func TestMCPStdioRequiresCompleteResponseLinesForTheCurrentRequest(t *testing.T) {
	if os.Getenv("AGENSTRA_MCP_STDIO_RESPONSE_HELPER") == "1" {
		var request JSON
		if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
			t.Fatal(err)
		}
		reply := JSON{"jsonrpc": "2.0", "id": request["id"], "result": JSON{"capacity": 2400}}
		switch os.Getenv("AGENSTRA_MCP_STDIO_RESPONSE_CASE") {
		case "missing-version":
			delete(reply, "jsonrpc")
		case "wrong-version":
			reply["jsonrpc"] = "1.0"
		case "result-and-null-error":
			reply["error"] = nil
		case "result-and-null-method":
			reply["method"] = nil
		}
		raw, err := json.Marshal(reply)
		if err != nil {
			t.Fatal(err)
		}
		switch os.Getenv("AGENSTRA_MCP_STDIO_RESPONSE_CASE") {
		case "server-request":
			_, _ = fmt.Fprintf(os.Stdout, "{\"jsonrpc\":\"2.0\",\"id\":%v,\"method\":\"ping\"}\n", request["id"])
		case "notification":
			_, _ = fmt.Fprintln(os.Stdout, `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`)
		case "trailing-garbage":
			_, _ = fmt.Fprintf(os.Stdout, "%s invalid\n", raw)
			return
		case "second-value":
			_, _ = fmt.Fprintf(os.Stdout, "%s %s\n", raw, raw)
			return
		}
		_, _ = fmt.Fprintf(os.Stdout, "%s \t\n", raw)
		return
	}
	for _, tc := range []struct {
		name      string
		wantError bool
	}{
		{"valid", false},
		{"notification", false},
		{"server-request", false},
		{"missing-version", true},
		{"wrong-version", true},
		{"result-and-null-error", true},
		{"result-and-null-method", true},
		{"trailing-garbage", true},
		{"second-value", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestMCPStdioRequiresCompleteResponseLinesForTheCurrentRequest$")
			cmd.Env = append(os.Environ(), "AGENSTRA_MCP_STDIO_RESPONSE_HELPER=1", "AGENSTRA_MCP_STDIO_RESPONSE_CASE="+tc.name)
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
			client := &stdioMCP{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}
			defer func() {
				_ = client.Close()
				if cmd.ProcessState == nil {
					t.Error("test subprocess was not reaped")
				}
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			result, err := client.Request(ctx, "tools/call", JSON{})
			if tc.wantError {
				if err == nil {
					t.Fatalf("malformed line became a confirmed result: %v", result)
				}
			} else if err != nil || fmt.Sprint(result["capacity"]) != "2400" {
				t.Fatalf("valid response lost: %v %v", result, err)
			}
		})
	}
}
