package packages

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestInstalledReadsDpkgStatus(t *testing.T) {
	installed, held, sources, err := Installed("testdata/status")
	if err != nil {
		t.Fatal(err)
	}
	want := []Package{
		{"libc6", "2.36-9+deb12u7", "amd64"},
		{"libc6", "2.36-9+deb12u7", "i386"},
		{"linux-image-amd64", "6.1.106-3", "amd64"},
		{"openssl", "3.0.14-1~deb12u1", "amd64"},
	}
	if !reflect.DeepEqual(installed, want) {
		t.Fatalf("installed %v\nwant %v", installed, want)
	}
	if !reflect.DeepEqual(held, []string{"linux-image-amd64"}) {
		t.Fatalf("held %v", held)
	}
	if !reflect.DeepEqual(sources, map[string]string{"libc6": "glibc", "linux-image-amd64": "linux-signed-amd64"}) {
		t.Fatalf("sources %v", sources)
	}
}

func TestRebootRequiredReadsTheFlagAndItsPackages(t *testing.T) {
	r, err := RebootRequired("testdata/reboot-required")
	if err != nil {
		t.Fatal(err)
	}
	if !r.Required || !reflect.DeepEqual(r.Packages, []string{"libc6", "linux-image-6.1.0-26-amd64"}) {
		t.Fatalf("got %+v", r)
	}

	r, err = RebootRequired(filepath.Join(t.TempDir(), "reboot-required"))
	if err != nil || r.Required || r.Packages == nil || len(r.Packages) != 0 {
		t.Fatalf("no flag: got %+v, %v", r, err)
	}
}

func TestLastUpgradeIsTheNewestUpgradeInDpkgsLog(t *testing.T) {
	at := LastUpgrade("testdata/dpkg.log")
	want := time.Date(2026, 10, 11, 3, 12, 40, 0, time.Local).UTC()
	if at == nil || !at.Equal(want) {
		t.Fatalf("got %v, want %v", at, want)
	}
}

func TestLastUpgradeLooksIntoTheRotatedLog(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "dpkg.log"), []byte("2026-10-12 01:00:00 install curl:amd64 <none> 1\n"), 0o644)
	b, _ := os.ReadFile("testdata/dpkg.log")
	os.WriteFile(filepath.Join(dir, "dpkg.log.1"), b, 0o644)
	if at := LastUpgrade(filepath.Join(dir, "dpkg.log")); at == nil || !at.Equal(time.Date(2026, 10, 11, 3, 12, 40, 0, time.Local)) {
		t.Fatalf("got %v", at)
	}
}

func TestALogItCannotReadCountsAsNone(t *testing.T) {
	if at := LastUpgrade(filepath.Join(t.TempDir(), "dpkg.log")); at != nil {
		t.Fatalf("no log: got %v", at)
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "dpkg.log")
	os.WriteFile(path, []byte("2026-10-11 03:12:40 upgrade a:amd64 1 2\n"), 0o000)
	if os.Getuid() != 0 {
		if at := LastUpgrade(path); at != nil {
			t.Fatalf("unreadable log: got %v", at)
		}
	}
}
