package system

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadsTheMachineFromItsFiles(t *testing.T) {
	info, err := Read(Paths{Root: "testdata/debian"})
	if err != nil {
		t.Fatal(err)
	}
	want := Info{
		Hostname: "web-1", OSID: "debian", OSVersionID: "12", OSCodename: "bookworm",
		OSName: "Debian GNU/Linux 12 (bookworm)", Arch: "amd64", Kernel: "6.1.0-25-amd64",
	}
	got := info
	got.MachineIDHash = ""
	if got != want {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}
}

func TestTheMachineIDNeverLeavesInTheClear(t *testing.T) {
	hash, err := MachineIDHash("testdata/debian")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hash, "3f1a9c2e7b5d4c0e8a6f1b2c3d4e5f60") {
		t.Fatal("the hash contains the machine ID")
	}
	mac := hmac.New(sha256.New, []byte("patchpilot-agent-machine-id"))
	mac.Write([]byte("3f1a9c2e7b5d4c0e8a6f1b2c3d4e5f60"))
	if hash != hex.EncodeToString(mac.Sum(nil)) {
		t.Fatalf("hash %s is not the documented HMAC (PROTOCOL.md)", hash)
	}
}

func TestUbuntuNamesItsCodenameAsWell(t *testing.T) {
	values, err := parseOSRelease(strings.NewReader("ID=ubuntu\nVERSION_ID=\"18.04\"\nUBUNTU_CODENAME=bionic\n# a comment\n"))
	if err != nil {
		t.Fatal(err)
	}
	if values["ID"] != "ubuntu" || values["VERSION_ID"] != "18.04" || values["UBUNTU_CODENAME"] != "bionic" {
		t.Fatalf("got %v", values)
	}
}

func TestRaspberryPiOSIsToldApartFromDebian(t *testing.T) {
	root := t.TempDir()
	for path, content := range map[string]string{
		"/proc/sys/kernel/hostname":  "pi-flur\n",
		"/proc/sys/kernel/osrelease": "6.6.51+rpt-rpi-v8\n",
		"/etc/machine-id":            "00000000000000000000000000000001\n",
		"/dpkg-architecture":         "arm64\n",
		"/etc/os-release":            "ID=debian\nVERSION_ID=\"12\"\nVERSION_CODENAME=bookworm\n",
		"/etc/rpi-issue":             "Raspberry Pi reference 2024-11-19\n",
	} {
		os.MkdirAll(filepath.Dir(root+path), 0o755)
		os.WriteFile(root+path, []byte(content), 0o644)
	}
	info, err := Read(Paths{Root: root})
	if err != nil || info.OSID != "debian" || info.OSVariant != "raspios" {
		t.Fatalf("got %+v, %v", info, err)
	}

	plain, _ := Read(Paths{Root: "testdata/debian"})
	if plain.OSVariant != "" {
		t.Fatalf("plain Debian got variant %q", plain.OSVariant)
	}
}
