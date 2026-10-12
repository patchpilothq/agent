package step

import (
	"os"
	"path/filepath"
	"testing"
)

func TestARequestIsAnEmptyFileNamedAfterTheMode(t *testing.T) {
	dir := t.TempDir()
	for _, mode := range []string{Refresh, Security, Upgrade} {
		if err := Request(dir, mode); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(dir, mode))
		if err != nil || info.Size() != 0 {
			t.Fatalf("%s: %v %v", mode, info, err)
		}
		if err := Request(dir, mode); err != nil {
			t.Fatalf("%s asked twice: %v", mode, err)
		}
	}
}

func TestNoOtherRequestCanBeMade(t *testing.T) {
	dir := t.TempDir()
	for _, mode := range []string{"", "reboot", "../upgrade", "upgrade; rm -rf /"} {
		if err := Request(dir, mode); err == nil {
			t.Errorf("%q was accepted", mode)
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Fatalf("files were created: %v", entries)
	}
}
