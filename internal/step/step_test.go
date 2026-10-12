package step

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestARequestIsAnEmptyFileNamedAfterTheMode(t *testing.T) {
	dir := t.TempDir()
	for _, mode := range []string{Refresh, Security, Upgrade, Stop} {
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

func TestTheListsAreAsTheStepRecordedThem(t *testing.T) {
	dir := t.TempDir()
	if l := ReadLists(dir); l.RefreshedAt != nil || l.Error != "" || !l.CheckedAt.IsZero() {
		t.Fatalf("nothing recorded, got %+v", l)
	}

	refreshed := filepath.Join(dir, "lists-refreshed")
	os.WriteFile(refreshed, []byte("2026-10-12T03:00:41Z\n"), 0o640)
	at := time.Date(2026, 10, 12, 3, 0, 41, 0, time.UTC)
	os.Chtimes(refreshed, at, at)
	l := ReadLists(dir)
	if l.RefreshedAt == nil || !l.RefreshedAt.Equal(at) || l.Error != "" || !l.CheckedAt.Equal(at) {
		t.Fatalf("a refresh, got %+v", l)
	}

	failed := filepath.Join(dir, "lists-error")
	os.WriteFile(failed, []byte("W: Failed to fetch http://deb.debian.org/debian/dists/buster/InRelease  404  Not Found\n\xff\n"), 0o640)
	later := at.Add(7 * time.Hour)
	os.Chtimes(failed, later, later)
	l = ReadLists(dir)
	if !l.RefreshedAt.Equal(at) || !l.CheckedAt.Equal(later) {
		t.Fatalf("a failure after a refresh, got %+v", l)
	}
	if l.Error != "W: Failed to fetch http://deb.debian.org/debian/dists/buster/InRelease  404  Not Found\n?" {
		t.Fatalf("error %q", l.Error)
	}

	os.WriteFile(failed, []byte(strings.Repeat("E: x\n", 1000)), 0o640)
	if l := ReadLists(dir); len(l.Error) > maxListsError {
		t.Fatalf("read %d bytes of apt's lines", len(l.Error))
	}

	os.WriteFile(refreshed, []byte("yesterday\n"), 0o640)
	os.Remove(failed)
	if l := ReadLists(dir); l.RefreshedAt != nil || l.CheckedAt.IsZero() {
		t.Fatalf("an unreadable time is no refresh, but a check: %+v", l)
	}
}
