package mcptransport

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
		if _, callErr := fmt.Fprintln(os.Stdout, "ready"); callErr != nil {
			t.Error(callErr)
		}
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
	client := &Stdio{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}
	defer func(close func() error) {
		if err := close(); err != nil {
			t.Error(err)
		}
	}(client.Close)
	if line, err := client.stdout.ReadString('\n'); err != nil || line != "ready\n" {
		t.Fatalf("helper failed to start: %q %v", line, err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		_, err := client.Request(ctx, "tools/call", map[string]any{"payload": strings.Repeat("x", 1<<20)})
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
		if callErr2 := client.Close(); callErr2 != nil {
			t.Error(callErr2)
		}
		<-finished
		t.Fatal("context deadline did not interrupt a blocked stdin write")
	}
}

func TestMCPStdioRequiresCompleteResponseLinesForTheCurrentRequest(t *testing.T) {
	if os.Getenv("AGENSTRA_MCP_STDIO_RESPONSE_HELPER") == "1" {
		var request map[string]any
		if err := json.NewDecoder(os.Stdin).Decode(&request); err != nil {
			t.Fatal(err)
		}
		reply := map[string]any{"jsonrpc": "2.0", "id": request["id"], "result": map[string]any{"capacity": 2400}}
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
			if _, callErr3 := fmt.Fprintf(os.Stdout, "{\"jsonrpc\":\"2.0\",\"id\":%v,\"method\":\"ping\"}\n", request["id"]); callErr3 != nil {
				t.Error(callErr3)
			}
		case "notification":
			if _, callErr4 := fmt.Fprintln(os.Stdout, `{"jsonrpc":"2.0","method":"notifications/progress","params":{"progress":1}}`); callErr4 != nil {
				t.Error(callErr4)
			}
		case "trailing-garbage":
			if _, callErr5 := fmt.Fprintf(os.Stdout, "%s invalid\n", raw); callErr5 != nil {
				t.Error(callErr5)
			}
			return
		case "second-value":
			if _, callErr6 := fmt.Fprintf(os.Stdout, "%s %s\n", raw, raw); callErr6 != nil {
				t.Error(callErr6)
			}
			return
		}
		if _, callErr7 := fmt.Fprintf(os.Stdout, "%s \t\n", raw); callErr7 != nil {
			t.Error(callErr7)
		}
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
			client := &Stdio{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}
			defer func() {
				if callErr8 := client.Close(); callErr8 != nil {
					t.Error(callErr8)
				}
				if cmd.ProcessState == nil {
					t.Error("test subprocess was not reaped")
				}
			}()
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			result, err := client.Request(ctx, "tools/call", map[string]any{})
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

func TestMCPStdioCancellationReapsProcess(t *testing.T) {
	if os.Getenv("AGENSTRA_MCP_HANG_HELPER") == "1" {
		reader := bufio.NewReader(os.Stdin)
		if _, callErr := reader.ReadString('\n'); callErr != nil {
			t.Error(callErr)
		}
		time.Sleep(30 * time.Second)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=TestMCPStdioCancellationReapsProcess")
	cmd.Env = append(os.Environ(), "AGENSTRA_MCP_HANG_HELPER=1")
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
	client := &Stdio{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = client.Request(ctx, "tools/list", map[string]any{})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wanted deadline, got %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("cancellation did not interrupt blocking read")
	}
	if cmd.ProcessState == nil {
		t.Fatal("MCP child not reaped")
	}
	if err := client.Close(); err != nil && !strings.Contains(err.Error(), "killed") {
		t.Fatalf("idempotent close: %v", err)
	}
}

func TestStdioFrameBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		limit       int
		want        string
		failed      bool
	}{
		{"exact", "1234\n", 5, "1234\n", false},
		{"oversize", "12345\n", 5, "", true},
		{"no newline", "123456", 5, "", true},
		{"partial EOF", "1234", 5, "1234", true},
		{"multiple buffer fragments", strings.Repeat("x", 64) + "\n", 65, strings.Repeat("x", 64) + "\n", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			line, err := readStdioFrame(bufio.NewReaderSize(strings.NewReader(tc.input), 16), tc.limit)
			if string(line) != tc.want || (err != nil) != tc.failed {
				t.Fatal(string(line), err)
			}
		})
	}
}

func TestStdioOversizeClosesAndReapsConnection(t *testing.T) {
	if os.Getenv("AGENSTRA_MCP_OVERSIZE_HELPER") == "1" {
		if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
			t.Fatal(err)
		}
		// A bounded invalid frame, without a delimiter; the client must stop reading.
		chunk := strings.Repeat("x", 4096)
		for range (16<<20)/len(chunk) + 1 {
			if _, err := fmt.Fprint(os.Stdout, chunk); err != nil {
				return
			}
		}
		time.Sleep(30 * time.Second)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestStdioOversizeClosesAndReapsConnection$")
	cmd.Env = append(os.Environ(), "AGENSTRA_MCP_OVERSIZE_HELPER=1")
	client, err := StartStdio(cmd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	if _, err := client.Request(ctx, "tools/call", map[string]any{}); err == nil || !strings.Contains(err.Error(), "response too large") {
		t.Fatal(err)
	}
	if cmd.ProcessState == nil {
		t.Fatal("oversized response subprocess was not reaped")
	}
	if _, err := client.Request(ctx, "tools/call", map[string]any{}); err == nil {
		t.Fatal("broken stream reused")
	}
}
