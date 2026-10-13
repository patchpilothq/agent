package packages

import (
	"encoding/json"
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
		{Name: "libc6", Version: "2.36-9+deb12u7", Arch: "amd64"},
		{Name: "libc6", Version: "2.36-9+deb12u7", Arch: "i386", Source: "glibc"},
		{Name: "linux-image-amd64", Version: "6.1.106-3", Arch: "amd64", Source: "linux-signed-amd64", SourceVersion: "6.1.106+3"},
		{Name: "openssl", Version: "3.0.14-1~deb12u1", Arch: "amd64"},
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

func TestAPackageIsTheProtocolsArrayOfThreeToFive(t *testing.T) {
	for _, c := range []struct {
		p    Package
		json string
	}{
		{Package{Name: "openssl", Version: "3.0.14", Arch: "amd64"}, `["openssl","3.0.14","amd64"]`},
		{Package{Name: "libssl3", Version: "3.0.14", Arch: "amd64", Source: "openssl"}, `["libssl3","3.0.14","amd64","openssl"]`},
		{Package{Name: "zlib1g", Version: "1:1.2.13-1+b1", Arch: "amd64", Source: "zlib", SourceVersion: "1:1.2.13-1"}, `["zlib1g","1:1.2.13-1+b1","amd64","zlib","1:1.2.13-1"]`},
		{Package{Name: "foo", Version: "1+b1", Arch: "all", SourceVersion: "1"}, `["foo","1+b1","all","foo","1"]`},
	} {
		b, _ := json.Marshal(c.p)
		if string(b) != c.json {
			t.Errorf("%+v: %s, want %s", c.p, b, c.json)
		}
		var back Package
		if err := json.Unmarshal(b, &back); err != nil || back.SourceName() != c.p.SourceName() || back.SourceVer() != c.p.SourceVer() || back.Version != c.p.Version {
			t.Errorf("%s read back as %+v (%v)", b, back, err)
		}
	}
	var p Package
	for _, bad := range []string{`["a","1"]`, `["a","1","all","b","2","x"]`, `{"name":"a"}`} {
		if json.Unmarshal([]byte(bad), &p) == nil {
			t.Errorf("%s was read", bad)
		}
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
