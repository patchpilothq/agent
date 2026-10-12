package run

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type sink struct {
	mu   sync.Mutex
	msgs []any
	fail bool
}

func (s *sink) send(msg any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return errors.New("not connected")
	}
	s.msgs = append(s.msgs, msg)
	return nil
}

func (s *sink) finished(t *testing.T) Finished {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		for _, m := range s.msgs {
			if f, ok := m.(Finished); ok {
				s.mu.Unlock()
				return f
			}
		}
		s.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("no result")
	return Finished{}
}

func (s *sink) progress() []Progress {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Progress
	for _, m := range s.msgs {
		if p, ok := m.(Progress); ok {
			out = append(out, p)
		}
	}
	return out
}

func runner(t *testing.T, s *sink) (*Runner, string) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "requests"), 0o755)
	return &Runner{
		RequestsDir: filepath.Join(dir, "requests"),
		OutputPath:  filepath.Join(dir, "output"),
		StatePath:   filepath.Join(dir, "run.json"),
		Send:        s.send,
		Log:         log.New(io.Discard, "", 0),
		StepActive:  func() bool { return true },
		Poll:        2 * time.Millisecond,
		Context:     context.Background(),
	}, dir
}

// writeStep writes output as the update step does, starting now.
func writeStep(t *testing.T, path, mode string, lines ...string) {
	content := "PATCHPILOT start " + mode + " " + time.Now().UTC().Format(time.RFC3339) + "\n"
	for _, l := range lines {
		content += l + "\n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestARunAsksTheStepAndReportsWhatItInstalled(t *testing.T) {
	s := &sink{}
	r, dir := runner(t, s)
	r.Handle(map[string]any{"type": "run", "run_id": "r1", "mode": "upgrade"})

	if _, err := os.Stat(filepath.Join(dir, "requests", "upgrade")); err != nil {
		t.Fatalf("the step was not asked: %v", err)
	}
	writeStep(t, r.OutputPath, "upgrade",
		"Hit:1 http://deb.debian.org/debian bookworm InRelease",
		"PATCHPILOT wait dpkg-lock",
		"PATCHPILOT package 1 3 openssl",
		"Setting up openssl ...",
		"PATCHPILOT package 2 3 libc6",
		"E: Sub-process /usr/bin/dpkg returned an error code (1)",
		"PATCHPILOT failed libc6 100",
		"PATCHPILOT package 3 3 tzdata",
		"PATCHPILOT done 1")

	f := s.finished(t)
	if f.RunID != "r1" || f.Result != "failed" {
		t.Fatalf("got %+v", f)
	}
	if len(f.Installed) != 2 || f.Installed[0] != "openssl" || f.Installed[1] != "tzdata" {
		t.Fatalf("installed %v", f.Installed)
	}
	if len(f.Failed) != 1 || f.Failed[0].Package != "libc6" || f.Failed[0].Output != "E: Sub-process /usr/bin/dpkg returned an error code (1)" {
		t.Fatalf("failed %+v", f.Failed)
	}

	states := map[string]bool{}
	for _, p := range s.progress() {
		states[p.State] = true
		if p.State == "installing" && p.Package == "libc6" && (p.Done != 1 || p.Total != 3) {
			t.Fatalf("progress %+v", p)
		}
	}
	if !states["refreshing"] || !states["waiting"] || !states["installing"] {
		t.Fatalf("states %v", states)
	}
	if _, err := os.Stat(r.StatePath); !os.IsNotExist(err) {
		t.Fatal("a sent result was kept")
	}
}

func TestAnOlderRunsOutputIsNotThisRuns(t *testing.T) {
	s := &sink{}
	r, _ := runner(t, s)
	old := "PATCHPILOT start upgrade " + time.Now().Add(-time.Hour).UTC().Format(time.RFC3339) + "\nPATCHPILOT done 0\n"
	os.WriteFile(r.OutputPath, []byte(old), 0o644)

	r.Handle(map[string]any{"type": "run", "run_id": "r2", "mode": "upgrade"})
	time.Sleep(30 * time.Millisecond)
	for _, m := range s.msgs {
		if _, ok := m.(Finished); ok {
			t.Fatal("finished on an older run's output")
		}
	}
	writeStep(t, r.OutputPath, "upgrade", "PATCHPILOT done 0")
	if f := s.finished(t); f.Result != "succeeded" || len(f.Installed) != 0 {
		t.Fatalf("got %+v", f)
	}
}

func TestAStoppedRunSaysSo(t *testing.T) {
	s := &sink{}
	r, dir := runner(t, s)
	r.Handle(map[string]any{"type": "run", "run_id": "r3", "mode": "security"})
	r.Handle(map[string]any{"type": "stop", "run_id": "r3"})
	if _, err := os.Stat(filepath.Join(dir, "requests", "stop")); err != nil {
		t.Fatal("the step was not asked to stop")
	}
	writeStep(t, r.OutputPath, "security", "PATCHPILOT package 1 2 openssl", "PATCHPILOT stopped", "PATCHPILOT done 0")
	if f := s.finished(t); f.Result != "stopped" || len(f.Installed) != 1 {
		t.Fatalf("got %+v", f)
	}
}

func TestOnlyTheRunsModesAreAccepted(t *testing.T) {
	s := &sink{}
	r, dir := runner(t, s)
	for _, msg := range []map[string]any{
		{"type": "run", "run_id": "x", "mode": "refresh"},
		{"type": "run", "run_id": "x", "mode": "reboot"},
		{"type": "run", "mode": "upgrade"},
		{"type": "shell", "command": "id"},
	} {
		r.Handle(msg)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "requests"))
	if len(entries) != 0 {
		t.Fatalf("requests made: %v", entries)
	}
}

func TestAResultThatCouldNotBeSentIsSentOnTheNextConnection(t *testing.T) {
	s := &sink{fail: true}
	r, _ := runner(t, s)
	r.Handle(map[string]any{"type": "run", "run_id": "r4", "mode": "upgrade"})
	writeStep(t, r.OutputPath, "upgrade", "PATCHPILOT done 0")

	deadline := time.Now().Add(5 * time.Second)
	for {
		var st state
		if b, err := os.ReadFile(r.StatePath); err == nil && json.Unmarshal(b, &st) == nil && st.Finished != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the result was not kept")
		}
		time.Sleep(5 * time.Millisecond)
	}

	s.mu.Lock()
	s.fail = false
	s.mu.Unlock()
	r.Resume()
	if f := s.finished(t); f.RunID != "r4" || f.Result != "succeeded" {
		t.Fatalf("got %+v", f)
	}
}

func TestAStepThatNeverStartsFailsTheRun(t *testing.T) {
	s := &sink{}
	r, _ := runner(t, s)
	r.StepActive = func() bool { return false }
	r.save(state{RunID: "r5", Mode: "upgrade", RequestedAt: time.Now().Add(-4 * time.Minute)})
	r.Resume()
	if f := s.finished(t); f.Result != "failed" || f.Output != "The update step did not start." {
		t.Fatalf("got %+v", f)
	}
}
