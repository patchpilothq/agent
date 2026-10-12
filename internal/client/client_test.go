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

// A server that checks the credential and records what the agent sends.
func socketServer(t *testing.T, got chan<- map[string]any) *httptest.Server {
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
		conn.Write(r.Context(), websocket.MessageText, []byte(`{"type":"welcome","server_id":"s1"}`))
		for {
			_, b, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			var msg map[string]any
			json.Unmarshal(b, &msg)
			got <- msg
		}
	}))
}

func TestTheSessionSaysHelloThenReportsAndReportsAgainOnChange(t *testing.T) {
	got := make(chan map[string]any, 10)
	srv := socketServer(t, got)
	defer srv.Close()

	var fingerprint, collected atomic.Int32
	s := &Session{
		Server:     srv.URL,
		Credential: Credential{"s1", "secret"},
		Version:    "1.2.3",
		Collect: func(context.Context) (any, error) {
			return map[string]any{"type": "report", "n": collected.Add(1)}, nil
		},
		Fingerprint: func() string { return string(rune('a' + fingerprint.Load())) },
		Log:         log.New(io.Discard, "", 0),
		CheckEvery:  10 * time.Millisecond,
		PingEvery:   time.Hour,
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	next := func() map[string]any {
		select {
		case m := <-got:
			return m
		case <-time.After(5 * time.Second):
			t.Fatal("nothing arrived")
			return nil
		}
	}
	if m := next(); m["type"] != "hello" || m["agent_version"] != "1.2.3" || m["protocol"] != float64(1) {
		t.Fatalf("first message %v", m)
	}
	if m := next(); m["type"] != "report" {
		t.Fatalf("second message %v", m)
	}
	select {
	case m := <-got:
		t.Fatalf("sent again without a change: %v", m)
	case <-time.After(100 * time.Millisecond):
	}
	fingerprint.Store(1)
	if m := next(); m["type"] != "report" || m["n"] != float64(2) {
		t.Fatalf("after a change %v", m)
	}
}

func TestARejectedCredentialEndsTheSession(t *testing.T) {
	srv := socketServer(t, make(chan map[string]any, 10))
	defer srv.Close()
	s := &Session{
		Server: srv.URL, Credential: Credential{"s1", "wrong"},
		Collect:     func(context.Context) (any, error) { return nil, nil },
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
