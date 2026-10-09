package engine

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func mcpStartupTool() JSON {
	return JSON{"name": "record.read", "description": "Read record", "inputSchema": JSON{"type": "object", "properties": JSON{}, "additionalProperties": false}, "outputSchema": JSON{"type": "object", "properties": JSON{"id": JSON{"type": "string"}}, "required": []any{"id"}, "additionalProperties": false}}
}

func TestMCPStdioStartupRespectsSourceTimeout(t *testing.T) {
	if mode := os.Getenv("AGENSTRA_MCP_STARTUP_HELPER"); mode != "" {
		if err := os.WriteFile(os.Getenv("AGENSTRA_MCP_STARTUP_PID"), []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
			t.Fatal(err)
		}
		reader := bufio.NewScanner(os.Stdin)
		for reader.Scan() {
			var request JSON
			if err := json.Unmarshal(reader.Bytes(), &request); err != nil {
				t.Fatal(err)
			}
			if request["method"] == mode {
				time.Sleep(30 * time.Second)
				return
			}
			var result JSON
			switch request["method"] {
			case "initialize":
				result = JSON{"protocolVersion": "2025-03-26", "capabilities": JSON{}, "serverInfo": JSON{"name": "test", "version": "1"}}
			case "notifications/initialized":
				continue
			case "tools/list":
				result = JSON{"tools": []any{mcpStartupTool()}}
			case "tools/call":
				result = JSON{"content": []any{}, "structuredContent": JSON{"id": "R1"}}
			default:
				t.Fatalf("unexpected method: %v", request["method"])
			}
			if err := json.NewEncoder(os.Stdout).Encode(JSON{"jsonrpc": "2.0", "id": request["id"], "result": result}); err != nil {
				t.Fatal(err)
			}
		}
		return
	}
	for _, stage := range []string{"initialize", "tools/list", "healthy"} {
		t.Run(stage, func(t *testing.T) {
			pidFile := t.TempDir() + "/pid"
			tool := mcpStartupTool()
			manifest := JSON{"schema": "agenstra.mcp-pack.v1", "name": "records", "version": "1", "guidance": "Read records", "source": JSON{"transport": "stdio", "command": os.Args[0], "args": []any{"-test.run=^TestMCPStdioStartupRespectsSourceTimeout$"}, "environment": JSON{"AGENSTRA_MCP_STARTUP_HELPER": "HELPER_MODE", "AGENSTRA_MCP_STARTUP_PID": "HELPER_PID"}, "timeout_seconds": 0.25}, "tools": []any{JSON{"name": tool["name"], "effect": "read", "contract_sha256": MCPContractDigest(tool)}}}
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			start := time.Now()
			pack, err := OpenMCPPack(ctx, writeTestManifest(t, manifest), map[string]string{"HELPER_MODE": stage, "HELPER_PID": pidFile})
			elapsed := time.Since(start)
			t.Cleanup(func() {
				raw, err := os.ReadFile(pidFile)
				if err != nil {
					t.Error(err)
					return
				}
				pid, err := strconv.Atoi(string(raw))
				if err != nil {
					t.Error(err)
					return
				}
				process, err := os.FindProcess(pid)
				if err != nil {
					t.Error(err)
					return
				}
				defer func(close func() error) {
					if err := close(); err != nil {
						t.Error(err)
					}
				}(process.Release)
				if err := process.Signal(syscall.Signal(0)); err == nil {
					t.Error("MCP startup subprocess is still running")
				}
			})
			if pack != nil {
				t.Cleanup(func() {
					if callErr := pack.Close(); callErr != nil {
						t.Error(callErr)
					}
				})
			}
			if stage != "healthy" {
				if !errors.Is(err, context.DeadlineExceeded) || elapsed > 2*time.Second || ctx.Err() != nil {
					t.Fatalf("configured startup timeout was ignored: pack=%v err=%v elapsed=%s parent=%v", pack, err, elapsed, ctx.Err())
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			result, err := pack.Invoke(ctx, "record.read", JSON{}, nil)
			if err != nil || result.ErrorCode != "" || result.Data["id"] != "R1" {
				t.Fatalf("completed startup cancelled a healthy connection: %+v %v", result, err)
			}
			if err := pack.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
