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
	"time"
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
