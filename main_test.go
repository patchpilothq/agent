package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runArgs(args ...string) (int, string, string) {
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), args, strings.NewReader(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func TestVersionPrintsTheBuildVersion(t *testing.T) {
	code, stdout, stderr := runArgs("--version")
	if code != 0 {
		t.Fatalf("exit code %d, stderr %q", code, stderr)
	}
	if stdout != version+"\n" {
		t.Fatalf("stdout %q, want %q", stdout, version+"\n")
	}
}

func TestAnythingElseIsRefusedWithUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"--version", "extra"}, {"shell"}, {"run", "extra"}} {
		code, stdout, stderr := runArgs(args...)
		if code != 2 {
			t.Errorf("%q: exit code %d, want 2", args, code)
		}
		if stdout != "" || stderr == "" {
			t.Errorf("%q: want usage on stderr only, got stdout %q stderr %q", args, stdout, stderr)
		}
	}
}

func TestTheServerComesFromTheConfiguration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agent.conf")

	os.WriteFile(path, []byte("# written by install.sh\nserver = https://patchpilot.cc/\n"), 0o644)
	if got, err := readServer(path); err != nil || got != "https://patchpilot.cc" {
		t.Errorf("got %q, %v", got, err)
	}

	for _, content := range []string{"", "server=\n", "server=ftp://example.com\n", "srv=https://x\n"} {
		os.WriteFile(path, []byte(content), 0o644)
		if got, err := readServer(path); err == nil {
			t.Errorf("%q: got %q, want an error", content, got)
		}
	}
}

func TestEnrollWantsATokenOnStandardInput(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "agent.conf")
	os.WriteFile(conf, []byte("server=https://patchpilot.invalid\n"), 0o644)
	var stdout, stderr bytes.Buffer
	code := run(context.Background(), []string{"enroll", "--config", conf, "--credential", filepath.Join(dir, "c")}, strings.NewReader("\n"), &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "no token") {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
}
