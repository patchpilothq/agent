package client

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func TestEnrollSendsTheTokenAndReturnsTheCredential(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)
		if r.URL.Path != "/api/agents/enroll" || r.Method != http.MethodPost {
			t.Errorf("%s %s", r.Method, r.URL.Path)
		}
		if body["token"] != "tok" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if body["hostname"] != "web-1" || body["machine_id_hash"] != "hash" {
			t.Errorf("body %v", body)
		}
		w.WriteHeader(http.StatusCreated)
		w.Write([]byte(`{"server_id":"s1","credential":"secret"}`))
	}))
	defer srv.Close()

	cred, err := Enroll(context.Background(), srv.URL, "tok", "web-1", "hash")
	if err != nil || cred != (Credential{"s1", "secret"}) {
		t.Fatalf("got %+v, %v", cred, err)
	}
	if _, err := Enroll(context.Background(), srv.URL, "wrong", "web-1", "hash"); err != ErrUnknownToken {
		t.Fatalf("a wrong token: %v", err)
	}
}

func TestTheCredentialIsReadableByItsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	if err := SaveCredential(path, Credential{"s1", "secret"}); err != nil {
		t.Fatal(err)
	}
	info, _ := filepath.Glob(path)
	if len(info) != 1 {
		t.Fatal("not written")
	}
	if st, _ := statMode(path); st != 0o600 {
		t.Fatalf("mode %o", st)
	}
	if cred, err := LoadCredential(path); err != nil || cred.Secret != "secret" {
		t.Fatalf("got %+v, %v", cred, err)
	}
}

// A server that checks the credential, sends welcome, records what the
// agent sends, and answers with what answer returns.
func socketServer(t *testing.T, got chan<- map[string]any, welcome string, answer func(map[string]any) string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/agents/socket" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer s1.secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		conn.Write(r.Context(), websocket.MessageText, []byte(welcome))
		for {
			_, b, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var msg map[string]any
			json.Unmarshal(b, &msg)
			got <- msg
			if answer != nil {
				if reply := answer(msg); reply != "" {
					conn.Write(r.Context(), websocket.MessageText, []byte(reply))
				}
			}
		}
	}))
}

// A state named by its version, which changes with the fingerprint.
type fakeState struct{ version string }

func (f fakeState) Digest() string { return f.version }
func (f fakeState) Full() any      { return map[string]any{"type": "report", "v": f.version} }
func (f fakeState) DeltaFrom(base State) any {
	return map[string]any{"type": "report_delta", "base": base.Digest(), "digest": f.version}
}

// ack acknowledges every report and delta, as the server does.
func ack(msg map[string]any) string {
	switch msg["type"] {
	case "report":
		return `{"type":"report_ack","digest":"` + msg["v"].(string) + `"}`
	case "report_delta":
		return `{"type":"report_ack","digest":"` + msg["digest"].(string) + `"}`
	}
	return ""
}

func session(srv *httptest.Server, version *atomic.Int32) *Session {
	return &Session{
		Server:     srv.URL,
		Credential: Credential{"s1", "secret"},
		Version:    "1.2.3",
		Collect: func(context.Context) (State, error) {
			return fakeState{string(rune('a' + version.Load()))}, nil
		},
		Fingerprint: func() string { return string(rune('a' + version.Load())) },
		Log:         log.New(io.Discard, "", 0),
		CheckEvery:  10 * time.Millisecond,
		PingEvery:   time.Hour,
	}
}

func receive(t *testing.T, got <-chan map[string]any) map[string]any {
	t.Helper()
	select {
	case m := <-got:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("nothing arrived")
		return nil
	}
}

func silence(t *testing.T, got <-chan map[string]any) {
	t.Helper()
	select {
	case m := <-got:
		t.Fatalf("sent without a reason: %v", m)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestAServerWithoutDigestsGetsTheWholeReportOnEveryChange(t *testing.T) {
	got := make(chan map[string]any, 10)
	srv := socketServer(t, got, `{"type":"welcome","server_id":"s1"}`, nil)
	defer srv.Close()
	var version atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go session(srv, &version).Run(ctx)

	if m := receive(t, got); m["type"] != "hello" || m["agent_version"] != "1.2.3" || m["protocol"] != float64(1) {
		t.Fatalf("first message %v", m)
	}
	if m := receive(t, got); m["type"] != "report" || m["v"] != "a" {
		t.Fatalf("second message %v", m)
	}
	silence(t, got)
	version.Store(1)
	if m := receive(t, got); m["type"] != "report" || m["v"] != "b" {
		t.Fatalf("after a change %v", m)
	}
}

func TestAfterTheFirstReportOnlyTheChangesAreSent(t *testing.T) {
	got := make(chan map[string]any, 10)
	srv := socketServer(t, got, `{"type":"welcome","server_id":"s1","report_digest":null}`, ack)
	defer srv.Close()
	var version atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go session(srv, &version).Run(ctx)

	receive(t, got) // hello
	if m := receive(t, got); m["type"] != "report" || m["v"] != "a" {
		t.Fatalf("first report %v", m)
	}
	silence(t, got)
	version.Store(1)
	if m := receive(t, got); m["type"] != "report_delta" || m["base"] != "a" || m["digest"] != "b" {
		t.Fatalf("after a change %v", m)
	}
	silence(t, got)
}

func TestAReconnectSendsNothingWhenTheServerHoldsTheState(t *testing.T) {
	got := make(chan map[string]any, 10)
	srv := socketServer(t, got, `{"type":"welcome","server_id":"s1","report_digest":"a"}`, ack)
	defer srv.Close()
	var version atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go session(srv, &version).Run(ctx)

	receive(t, got) // hello
	silence(t, got)
	version.Store(1)
	if m := receive(t, got); m["type"] != "report_delta" || m["base"] != "a" || m["digest"] != "b" {
		t.Fatalf("after a change %v", m)
	}
}

func TestTheServerCanAskForTheWholeReportAtAnyTime(t *testing.T) {
	got := make(chan map[string]any, 10)
	asked := false
	srv := socketServer(t, got, `{"type":"welcome","server_id":"s1","report_digest":"a"}`, func(msg map[string]any) string {
		if msg["type"] == "hello" && !asked {
			asked = true
			return `{"type":"report_needed"}`
		}
		return ack(msg)
	})
	defer srv.Close()
	var version atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go session(srv, &version).Run(ctx)

	receive(t, got) // hello
	if m := receive(t, got); m["type"] != "report" || m["v"] != "a" {
		t.Fatalf("asked for the whole report, got %v", m)
	}
}

func TestOneDeltaAtATime(t *testing.T) {
	got := make(chan map[string]any, 10)
	srv := socketServer(t, got, `{"type":"welcome","server_id":"s1","report_digest":null}`, nil)
	defer srv.Close()
	var version atomic.Int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go session(srv, &version).Run(ctx)

	receive(t, got) // hello
	receive(t, got) // the report, never acknowledged
	version.Store(1)
	silence(t, got)
}

func TestTheListsGoAsTheirOwnMessageWhenTheyChange(t *testing.T) {
	got := make(chan map[string]any, 10)
	srv := socketServer(t, got, `{"type":"welcome","server_id":"s1","report_digest":"a"}`, ack)
	defer srv.Close()
	var version, refreshed atomic.Int32
	s := session(srv, &version)
	s.Lists = func() any { return map[string]any{"type": "lists", "n": refreshed.Load()} }
	s.Fingerprint = func() string { return string(rune('a'+version.Load())) + string(rune('a'+refreshed.Load())) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	receive(t, got) // hello
	if m := receive(t, got); m["type"] != "lists" || m["n"] != float64(0) {
		t.Fatalf("on connecting %v", m)
	}
	silence(t, got)
	refreshed.Store(1)
	if m := receive(t, got); m["type"] != "lists" || m["n"] != float64(1) {
		t.Fatalf("after a refresh %v", m)
	}
	silence(t, got)
}

func TestARejectedCredentialEndsTheSession(t *testing.T) {
	srv := socketServer(t, make(chan map[string]any, 10), `{"type":"welcome","server_id":"s1"}`, nil)
	defer srv.Close()
	s := &Session{
		Server: srv.URL, Credential: Credential{"s1", "wrong"},
		Collect:     func(context.Context) (State, error) { return fakeState{}, nil },
		Fingerprint: func() string { return "" },
		Log:         log.New(io.Discard, "", 0),
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Run(ctx); err != ErrRemoved {
		t.Fatalf("got %v, want ErrRemoved", err)
	}
}

func TestSocketURLFollowsTheServersScheme(t *testing.T) {
	for in, want := range map[string]string{
		"https://patchpilot.cc": "wss://patchpilot.cc/api/agents/socket",
		"http://10.0.2.2:4000/": "ws://10.0.2.2:4000/api/agents/socket",
	} {
		if got := socketURL(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}
