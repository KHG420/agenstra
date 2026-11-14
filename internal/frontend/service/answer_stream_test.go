package service

import (
	"bufio"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	agentcontract "github.com/KHG420/agenstra/internal/contract/agent"
)

func TestWebAnswerStreamAuthenticationAndDurableReconnect(t *testing.T) {
	f := newWebFixture(t, &hostModel{}, false)
	run := f.run(t)
	s := &HTTPServer{Host: f.h, Web: f.w, stop: make(chan struct{})}
	alice, err := f.w.MintSession("alice")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := f.w.MintSession("bob")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		token  string
		status int
	}{{"", 401}, {bob, 404}, {alice, 200}, {alice, 200}} {
		req := httptest.NewRequest("GET", "/web/v1/runs/"+run.RunID+"/answer-stream", nil)
		req.Header.Set("Authorization", "Bearer "+tc.token)
		out := httptest.NewRecorder()
		s.webHTTP(out, req)
		if out.Code != tc.status {
			t.Fatal(tc.status, out.Code, out.Body.String())
		}
		if tc.status == 200 && (!strings.Contains(out.Body.String(), `"phase":"completed"`) || !strings.Contains(out.Body.String(), `"text":"Done"`) || !strings.HasPrefix(out.Header().Get("Content-Type"), "text/event-stream")) {
			t.Fatal("durable answer missing", out.Body.String())
		}
	}
}

func TestWaitingAnswerStreamBoundsAuthorizationReadsAndRechecksRevocation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newWebFixture(t, &hostModel{}, false)
		run, err := f.w.CreateBrowserRun(t.Context(), "alice", "records-web", f.session.ID, "Respond", "idle-stream")
		if err != nil {
			t.Fatal(err)
		}
		resolve := f.h.PolicyResolver
		var reads atomic.Int32
		var revoked atomic.Bool
		f.h.PolicyResolver = func(ctx context.Context, owner, pack string) (agentcontract.ExecutionPolicy, error) {
			reads.Add(1)
			if revoked.Load() {
				return agentcontract.ExecutionPolicy{}, agentcontract.NewHostError("access_denied")
			}
			return resolve(ctx, owner, pack)
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		req := httptest.NewRequestWithContext(ctx, "GET", "/runs/"+run.RunID+"/answer-stream", nil)
		out := httptest.NewRecorder()
		s := &HTTPServer{Host: f.h, stop: make(chan struct{})}
		var exited atomic.Bool
		go func() {
			s.answerHTTP(out, req, "alice", nil)
			exited.Store(true)
		}()
		synctest.Wait()
		time.Sleep(900 * time.Millisecond)
		synctest.Wait()
		if reads.Load() != 1 || exited.Load() {
			t.Fatalf("idle stream repeatedly queried authorization: reads=%d exited=%t", reads.Load(), exited.Load())
		}
		// No policy result is cached. The next snapshot must check revocation
		// before publishing any newly available answer.
		revoked.Store(true)
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		if reads.Load() != 2 || !exited.Load() {
			t.Fatalf("revocation did not close the stream: reads=%d exited=%t", reads.Load(), exited.Load())
		}
		if !strings.Contains(out.Body.String(), `"phase":"failed"`) {
			t.Fatalf("revocation did not publish failure: %s", out.Body.String())
		}
	})
}

func TestAnswerStreamTemporaryAuthorizationFailureCanReconnect(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		f := newWebFixture(t, &hostModel{}, false)
		run, err := f.w.CreateBrowserRun(t.Context(), "alice", "records-web", f.session.ID, "Respond", "temporary-stream")
		if err != nil {
			t.Fatal(err)
		}
		resolve := f.h.PolicyResolver
		var reads atomic.Int32
		f.h.PolicyResolver = func(ctx context.Context, owner, pack string) (agentcontract.ExecutionPolicy, error) {
			if reads.Add(1) == 2 {
				return agentcontract.ExecutionPolicy{}, agentcontract.NewHostError("authorization_unavailable")
			}
			return resolve(ctx, owner, pack)
		}
		s := &HTTPServer{Host: f.h, stop: make(chan struct{})}
		out := httptest.NewRecorder()
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.answerHTTP(out, httptest.NewRequest("GET", "/runs/"+run.RunID+"/answer-stream", nil), "alice", nil)
		}()
		synctest.Wait()
		time.Sleep(time.Second)
		synctest.Wait()
		select {
		case <-done:
		default:
			t.Fatal("temporary read failure did not release the stream")
		}
		if strings.Contains(out.Body.String(), `"phase":"failed"`) {
			t.Fatal("temporary authorization failure terminated the answer watcher", out.Body.String())
		}
		if _, err = f.h.Drive(t.Context(), run.RunID, "alice"); err != nil {
			t.Fatal(err)
		}
		out = httptest.NewRecorder()
		s.answerHTTP(out, httptest.NewRequest("GET", "/runs/"+run.RunID+"/answer-stream", nil), "alice", nil)
		if !strings.Contains(out.Body.String(), `"phase":"completed"`) || !strings.Contains(out.Body.String(), `"text":"Done"`) {
			t.Fatal("reconnect did not recover the saved answer", out.Body.String())
		}
	})
}

func TestAnswerStreamDisconnectExpiryAndServerShutdown(t *testing.T) {
	for _, mode := range []string{"disconnect", "expiry", "shutdown"} {
		t.Run(mode, func(t *testing.T) {
			f := newWebFixture(t, &hostModel{}, false)
			run, err := f.w.CreateBrowserRun(t.Context(), "alice", "records-web", f.session.ID, "Respond", "stream")
			if err != nil {
				t.Fatal(err)
			}
			s := &HTTPServer{Host: f.h, stop: make(chan struct{})}
			var valid atomic.Bool
			valid.Store(true)
			exited := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(exited)
				s.answerHTTP(w, r, "alice", valid.Load)
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			req, err := http.NewRequestWithContext(ctx, "GET", server.URL+"/runs/"+run.RunID+"/answer-stream", nil)
			if err != nil {
				t.Fatal(err)
			}
			response, err := server.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := response.Body.Close(); err != nil {
					t.Error(err)
				}
			}()
			scanner := bufio.NewScanner(response.Body)
			if !scanner.Scan() || scanner.Text() != "event: answer" {
				t.Fatal("no initial snapshot")
			}
			switch mode {
			case "disconnect":
				cancel()
			case "expiry":
				valid.Store(false)
			case "shutdown":
				close(s.stop)
			}
			select {
			case <-exited:
			case <-time.After(2 * time.Second):
				t.Fatal("stream handler leaked")
			}
			if mode != "disconnect" {
				if _, err = io.ReadAll(response.Body); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
