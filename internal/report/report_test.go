package report

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// A whole machine in a directory: the system package's Debian fixture plus
// dpkg's status.
func machine(t *testing.T, osRelease string) string {
	root := t.TempDir()
	write := func(path, content string) {
		full := filepath.Join(root, path)
		os.MkdirAll(filepath.Dir(full), 0o755)
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("proc/sys/kernel/hostname", "web-1\n")
	write("proc/sys/kernel/osrelease", "6.8.0-45-generic\n")
	write("etc/machine-id", "0123456789abcdef0123456789abcdef\n")
	write("dpkg-architecture", "amd64\n")
	write("etc/os-release", osRelease)
	write("var/lib/dpkg/status", "Package: openssl\nStatus: install ok installed\nArchitecture: amd64\nVersion: 3.0.13\n")
	return root
}

func fakeApt(_ context.Context, args ...string) ([]byte, error) {
	return []byte("Inst openssl [3.0.13] (3.0.14 Ubuntu:24.04/noble-security [amd64])\n"), nil
}

func TestAReportCarriesExactlyThePublishedFields(t *testing.T) {
	root := machine(t, "ID=ubuntu\nVERSION_ID=\"24.04\"\nVERSION_CODENAME=noble\nPRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\n")
	r, err := Collect(context.Background(), Sources{
		Root: root, Apt: fakeApt,
		ProStatus: func(context.Context) ([]byte, error) {
			return []byte(`{"attached": true, "account": {"name": "x"}}`), nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(r)
	var fields map[string]json.RawMessage
	json.Unmarshal(b, &fields)
	var keys []string
	for k := range fields {
		keys = append(keys, k)
	}
	want := "held last_upgrade_at packages pro reboot system type updates"
	sort.Strings(keys)
	if got := strings.Join(keys, " "); got != want {
		t.Fatalf("fields %q, want %q (PROTOCOL.md)", got, want)
	}
	if r.Pro == nil || !r.Pro.Attached || r.System.OSID != "ubuntu" || len(r.Updates) != 1 || !r.Updates[0].Security {
		t.Fatalf("got %+v", r)
	}
	if strings.Contains(string(b), "account") {
		t.Fatal("the Pro status leaked more than whether it is attached")
	}
}

func TestProIsReportedOnUbuntuOnly(t *testing.T) {
	root := machine(t, "ID=debian\nVERSION_ID=\"12\"\n")
	called := false
	r, err := Collect(context.Background(), Sources{
		Root: root, Apt: fakeApt,
		ProStatus: func(context.Context) ([]byte, error) { called = true; return nil, nil },
	})
	if err != nil || r.Pro != nil || called {
		t.Fatalf("got %+v, %v, called %v", r.Pro, err, called)
	}
}
