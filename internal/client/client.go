// Package client is the agent's side of PROTOCOL.md: enrolling once, then
// one WebSocket to the server for as long as the agent runs.
package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Credential is what enrolling returns and every connection presents.
type Credential struct {
	ServerID string `json:"server_id"`
	Secret   string `json:"credential"`
}

// ErrRemoved means the server no longer accepts this agent's credential:
// the server was removed from its workspace, or enrolled again.
var ErrRemoved = errors.New("the PatchPilot server no longer knows this agent")

// ErrUnknownToken means the enrollment token is wrong or was replaced.
var ErrUnknownToken = errors.New("the enrollment token was not accepted")

// Enroll registers the machine with its workspace's token.
func Enroll(ctx context.Context, server, token, hostname, machineIDHash string) (Credential, error) {
	var cred Credential
	body, _ := json.Marshal(map[string]string{
		"token": token, "hostname": hostname, "machine_id_hash": machineIDHash,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(server, "/")+"/api/agents/enroll", bytes.NewReader(body))
	if err != nil {
		return cred, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return cred, fmt.Errorf("enrolling: %w", err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusCreated:
		if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&cred); err != nil {
			return cred, fmt.Errorf("enrolling: reading the answer: %w", err)
		}
		if cred.ServerID == "" || cred.Secret == "" {
			return cred, errors.New("enrolling: the answer carried no credential")
		}
		return cred, nil
	case http.StatusUnauthorized:
		return cred, ErrUnknownToken
	default:
		return cred, fmt.Errorf("enrolling: the server answered %s", resp.Status)
	}
}

// SaveCredential writes the credential readable by the agent's user only.
func SaveCredential(path string, cred Credential) error {
	b, err := json.Marshal(cred)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".credential-")
	if err != nil {
		return fmt.Errorf("saving the credential: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("saving the credential: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("saving the credential: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("saving the credential: %w", err)
	}
	return os.Rename(tmp.Name(), path)
}

// LoadCredential reads what SaveCredential wrote.
func LoadCredential(path string) (Credential, error) {
	var cred Credential
	b, err := os.ReadFile(path)
	if err != nil {
		return cred, fmt.Errorf("reading the credential: %w", err)
	}
	if err := json.Unmarshal(b, &cred); err != nil || cred.ServerID == "" || cred.Secret == "" {
		return cred, fmt.Errorf("reading the credential: %s is not one", path)
	}
	return cred, nil
}

// Session keeps the connection: it reconnects with growing waits, sends the
// report whenever it changes, and pings so that a dead connection is
// noticed.
type Session struct {
	Server     string
	Credential Credential
	Version    string
	// Collect reads a fresh report; Fingerprint is a cheap value that changes
	// when a report may have.
	Collect     func(ctx context.Context) (any, error)
	Fingerprint func() string
	Log         *log.Logger
	// OnMessage gets every message from the server but "welcome";
	// OnConnect runs after each hello. Both may be nil.
	OnMessage func(ctx context.Context, msg map[string]any)
	OnConnect func(ctx context.Context)

	// Zero values take the defaults below; tests shorten them.
	CheckEvery   time.Duration // how often the fingerprint is compared
	CollectEvery time.Duration // a full report at least this often
	PingEvery    time.Duration

	mu   sync.Mutex
	conn *websocket.Conn
}

// ErrNotConnected is what Send answers while there is no connection.
var ErrNotConnected = errors.New("not connected to PatchPilot")

// Send writes a message on the current connection, from any goroutine.
func (s *Session) Send(msg any) error {
	s.mu.Lock()
	conn := s.conn
	s.mu.Unlock()
	if conn == nil {
		return ErrNotConnected
	}
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return conn.Write(ctx, websocket.MessageText, b)
}

func (s *Session) setConn(conn *websocket.Conn) {
	s.mu.Lock()
	s.conn = conn
	s.mu.Unlock()
}

func (s *Session) defaults() {
	if s.CheckEvery == 0 {
		s.CheckEvery = 30 * time.Second
	}
	if s.CollectEvery == 0 {
		s.CollectEvery = 15 * time.Minute
	}
	if s.PingEvery == 0 {
		s.PingEvery = 30 * time.Second
	}
}

// Run connects and reconnects until ctx ends or the server removed the agent.
func (s *Session) Run(ctx context.Context) error {
	s.defaults()
	wait := time.Second
	for {
		started := time.Now()
		err := s.once(ctx)
		if errors.Is(err, ErrRemoved) || ctx.Err() != nil {
			return err
		}
		if time.Since(started) > time.Minute {
			wait = time.Second
		}
		s.Log.Printf("disconnected: %v; reconnecting in %s", err, wait)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait + time.Duration(rand.Int64N(int64(wait/2)+1))):
		}
		wait = min(wait*2, time.Minute)
	}
}

func (s *Session) once(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	conn, resp, err := websocket.Dial(ctx, socketURL(s.Server), &websocket.DialOptions{
		HTTPHeader: http.Header{"Authorization": {"Bearer " + s.Credential.ServerID + "." + s.Credential.Secret}},
	})
	if resp != nil && resp.StatusCode == http.StatusUnauthorized {
		return ErrRemoved
	}
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	s.setConn(conn)
	defer s.setConn(nil)

	readErr := make(chan error, 1)
	go func() { readErr <- s.read(ctx, conn) }()

	if err := s.send(ctx, conn, map[string]any{"type": "hello", "protocol": 1, "agent_version": s.Version}); err != nil {
		return err
	}

	var sent []byte
	report := func() error {
		r, err := s.Collect(ctx)
		if err != nil {
			s.Log.Printf("reading the machine's state: %v", err)
			return nil // the connection is fine; the next check tries again
		}
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if bytes.Equal(b, sent) {
			return nil
		}
		if err := conn.Write(ctx, websocket.MessageText, b); err != nil {
			return err
		}
		sent = b
		return nil
	}
	if err := report(); err != nil {
		return err
	}
	s.Log.Printf("connected to %s", s.Server)
	if s.OnConnect != nil {
		s.OnConnect(ctx)
	}

	fingerprint := s.Fingerprint()
	lastCollect := time.Now()
	check := time.NewTicker(s.CheckEvery)
	defer check.Stop()
	ping := time.NewTicker(s.PingEvery)
	defer ping.Stop()

	for {
		select {
		case <-ctx.Done():
			conn.Close(websocket.StatusNormalClosure, "")
			return ctx.Err()
		case err := <-readErr:
			return err
		case <-ping.C:
			pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
			err := conn.Ping(pctx)
			pcancel()
			if err != nil {
				return fmt.Errorf("ping: %w", err)
			}
		case <-check.C:
			if f := s.Fingerprint(); f != fingerprint || time.Since(lastCollect) >= s.CollectEvery {
				fingerprint, lastCollect = f, time.Now()
				if err := report(); err != nil {
					return err
				}
			}
		}
	}
}

func (s *Session) read(ctx context.Context, conn *websocket.Conn) error {
	for {
		_, b, err := conn.Read(ctx)
		if err != nil {
			return err
		}
		var msg map[string]any
		if json.Unmarshal(b, &msg) != nil {
			continue
		}
		switch msg["type"] {
		case "welcome":
			s.Log.Printf("the server knows this machine as %v", msg["server_id"])
		default:
			if s.OnMessage != nil {
				s.OnMessage(ctx, msg)
			}
		}
	}
}

func (s *Session) send(ctx context.Context, conn *websocket.Conn, msg any) error {
	b, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, b)
}

func socketURL(server string) string {
	server = strings.TrimRight(server, "/")
	switch {
	case strings.HasPrefix(server, "https://"):
		server = "wss://" + strings.TrimPrefix(server, "https://")
	case strings.HasPrefix(server, "http://"):
		server = "ws://" + strings.TrimPrefix(server, "http://")
	}
	return server + "/api/agents/socket"
}
