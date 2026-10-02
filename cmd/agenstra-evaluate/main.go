package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	agenstra "github.com/KHG420/agenstra"
)

type evaluationClient struct {
	base, key string
	http      *http.Client
}

func (c evaluationClient) request(ctx context.Context, method, path string, body, target any) error {
	var data []byte
	if body != nil {
		var err error
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(data))
	if err != nil {
		return errors.New("evaluation_request_invalid")
	}
	req.Header.Set("Authorization", "Bearer "+c.key)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := c.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New("evaluation_connection_failed")
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("evaluation_http_%d", res.StatusCode)
	}
	if target == nil {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(res.Body, 32<<20))
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return errors.New("evaluation_response_invalid")
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("evaluation_response_invalid")
	}
	return nil
}

func uncertainCreate(err error) bool {
	if err == nil {
		return false
	}
	code := err.Error()
	return code == "evaluation_connection_failed" || code == "evaluation_response_invalid" || strings.HasPrefix(code, "evaluation_http_5") || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}
func stopped(status string) bool {
	switch status {
	case "completed", "failed", "cancelled", "needs_input", "needs_approval", "needs_authorization", "needs_reconciliation":
		return true
	}
	return false
}
func (c evaluationClient) evaluate(ctx context.Context, test agenstra.EvaluationCase, timeout time.Duration) agenstra.EvaluationResult {
	result := agenstra.EvaluationResult{Name: test.Name, Checks: []agenstra.EvaluationCheck{}}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var run agenstra.StoredRun
	owned := test.RunID == ""
	if owned {
		requestID := test.RequestID
		if requestID == "" {
			requestID = agenstra.NewID()
		}
		result.RequestID = requestID
		body := map[string]any{"pack_id": test.PackID, "instruction": test.Instruction, "request_id": requestID}
		err := c.request(ctx, "POST", "/runs", body, &run)
		if err == nil && run.RunID == "" {
			err = errors.New("evaluation_response_invalid")
		}
		wasUncertain := uncertainCreate(err)
		if wasUncertain && ctx.Err() == nil {
			run = agenstra.StoredRun{}
			err = c.request(ctx, "POST", "/runs", body, &run)
			if err == nil && run.RunID == "" {
				err = errors.New("evaluation_response_invalid")
			}
		}
		if err != nil {
			result.ErrorCode = err.Error()
			if wasUncertain || uncertainCreate(err) {
				result.ErrorCode = "evaluation_create_outcome_unknown"
			}
			return result
		}
	} else {
		run.RunID = test.RunID
	}
	result.RunID = run.RunID
	path := "/runs/" + url.PathEscape(run.RunID)
	for {
		if err := c.request(ctx, "GET", path, nil, &run); err != nil {
			result.ErrorCode = err.Error()
			if owned {
				cleanup, done := context.WithTimeout(context.Background(), 5*time.Second)
				if err := c.request(cleanup, "POST", path+"/cancel", nil, nil); err != nil {
					result.ErrorCode += "; cancellation_request_failed"
				}
				done()
			}
			return result
		}
		if stopped(run.Status) {
			break
		}
		timer := time.NewTimer(250 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	checked, err := agenstra.EvaluateRun(ctx, run, test)
	if err != nil {
		result.ErrorCode = err.Error()
		return result
	}
	checked.RequestID = result.RequestID
	var diagnostics agenstra.RunDiagnostics
	if c.request(ctx, "GET", path+"/diagnostics", nil, &diagnostics) == nil {
		checked.Diagnostics = &diagnostics
	}
	return checked
}
func run(args []string, output io.Writer) (bool, error) {
	fs := flag.NewFlagSet("agenstra-evaluate", flag.ContinueOnError)
	server := fs.String("server", "http://127.0.0.1:8091", "Agenstra service URL")
	keyEnv := fs.String("key-env", "AGENSTRA_API_KEY", "user API key environment variable")
	casesPath := fs.String("cases", "", "JSON array of representative tasks and assertions")
	reportPath := fs.String("output", "", "optional JSON report path")
	timeout := fs.Duration("timeout", 2*time.Minute, "deadline per case; new timed-out runs receive a cancellation request")
	if err := fs.Parse(args); err != nil {
		return false, err
	}
	u, err := url.Parse(*server)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || *timeout <= 0 || *casesPath == "" || fs.NArg() != 0 {
		return false, errors.New("server, cases and timeout must be valid")
	}
	key := os.Getenv(*keyEnv)
	if key == "" {
		return false, fmt.Errorf("missing user API key in %s", *keyEnv)
	}
	raw, err := os.ReadFile(*casesPath)
	if err != nil {
		return false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	var cases []agenstra.EvaluationCase
	if err := decoder.Decode(&cases); err != nil {
		return false, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF || len(cases) == 0 {
		return false, errors.New("cases must be a non-empty JSON array")
	}
	for _, test := range cases {
		if err := test.Validate(); err != nil {
			return false, fmt.Errorf("case %q: %w", test.Name, err)
		}
	}
	client := evaluationClient{strings.TrimRight(*server, "/"), key, &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	results := []agenstra.EvaluationResult{}
	passed := true
	for _, test := range cases {
		if ctx.Err() != nil {
			break
		}
		result := client.evaluate(ctx, test, *timeout)
		passed = passed && result.Passed
		results = append(results, result)
	}
	passed = passed && len(results) == len(cases)
	report := map[string]any{"schema": "agenstra.integration-evaluation.v1", "passed": passed, "results": results}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return false, err
	}
	data = append(data, '\n')
	if *reportPath != "" {
		if err := os.WriteFile(*reportPath, data, 0600); err != nil {
			return false, err
		}
	}
	_, err = output.Write(data)
	return passed, err
}
func main() {
	passed, err := run(os.Args[1:], os.Stdout)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agenstra-evaluate:", err)
		os.Exit(2)
	}
	if !passed {
		os.Exit(1)
	}
}
