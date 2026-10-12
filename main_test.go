package main

import (
	"bytes"
	"testing"
)

func TestVersionPrintsTheBuildVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("exit code %d, stderr %q", code, stderr.String())
	}
	if got := stdout.String(); got != version+"\n" {
		t.Fatalf("stdout %q, want %q", got, version+"\n")
	}
}

func TestAnythingElseIsRefusedWithUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"--help"}, {"--version", "extra"}} {
		var stdout, stderr bytes.Buffer
		if code := run(args, &stdout, &stderr); code != 2 {
			t.Errorf("%q: exit code %d, want 2", args, code)
		}
		if stdout.Len() != 0 || stderr.Len() == 0 {
			t.Errorf("%q: want usage on stderr only, got stdout %q stderr %q", args, stdout.String(), stderr.String())
		}
	}
}
