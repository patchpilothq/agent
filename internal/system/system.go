// Package system reads what the agent reports about the machine itself: its
// name, its release, its architecture and kernel, and a hash of its machine
// ID. Everything here is readable by any user.
package system

import (
	"bufio"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Info is the "system" object of a report (PROTOCOL.md).
type Info struct {
	Hostname      string `json:"hostname"`
	OSID          string `json:"os_id"`
	OSVersionID   string `json:"os_version_id"`
	OSCodename    string `json:"os_codename"`
	OSName        string `json:"os_name"`
	OSVariant     string `json:"os_variant,omitempty"`
	Arch          string `json:"arch"`
	Kernel        string `json:"kernel"`
	MachineIDHash string `json:"machine_id_hash"`
}

// Paths says where the facts are read from; tests point it at fixtures.
type Paths struct {
	Root string // prefixed to every path; "" on a real machine
}

// machineIDKey keys the HMAC that stands for the machine ID, so that the ID
// itself never leaves the machine (PROTOCOL.md, "Enrolling").
const machineIDKey = "patchpilot-agent-machine-id"

// Read collects Info. The architecture comes from dpkg, which knows the
// name the archive uses (armhf, not arm).
func Read(p Paths) (Info, error) {
	var info Info

	hostname, err := readTrimmed(p.Root + "/proc/sys/kernel/hostname")
	if err != nil {
		return info, err
	}
	info.Hostname = hostname

	if info.Kernel, err = readTrimmed(p.Root + "/proc/sys/kernel/osrelease"); err != nil {
		return info, err
	}

	release, err := osRelease(p.Root)
	if err != nil {
		return info, err
	}
	info.OSID = release["ID"]
	info.OSVersionID = release["VERSION_ID"]
	info.OSCodename = release["VERSION_CODENAME"]
	if info.OSCodename == "" {
		info.OSCodename = release["UBUNTU_CODENAME"]
	}
	info.OSName = release["PRETTY_NAME"]
	// Raspberry Pi OS's 64-bit edition calls itself Debian in os-release;
	// the file its image builder leaves tells it apart.
	if _, err := os.Stat(p.Root + "/etc/rpi-issue"); err == nil {
		info.OSVariant = "raspios"
	}

	if info.MachineIDHash, err = MachineIDHash(p.Root); err != nil {
		return info, err
	}

	info.Arch, err = dpkgArch(p.Root)
	return info, err
}

// MachineIDHash is the hex HMAC-SHA256 of /etc/machine-id.
func MachineIDHash(root string) (string, error) {
	id, err := readTrimmed(root + "/etc/machine-id")
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(machineIDKey))
	io.WriteString(mac, id)
	return hex.EncodeToString(mac.Sum(nil)), nil
}

func osRelease(root string) (map[string]string, error) {
	f, err := os.Open(root + "/etc/os-release")
	if os.IsNotExist(err) {
		f, err = os.Open(root + "/usr/lib/os-release")
	}
	if err != nil {
		return nil, fmt.Errorf("reading os-release: %w", err)
	}
	defer f.Close()
	return parseOSRelease(f)
}

func parseOSRelease(r io.Reader) (map[string]string, error) {
	values := map[string]string{}
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		key, value, ok := strings.Cut(strings.TrimSpace(scanner.Text()), "=")
		if !ok || strings.HasPrefix(key, "#") {
			continue
		}
		values[key] = strings.Trim(value, `"'`)
	}
	return values, scanner.Err()
}

// dpkgArch asks dpkg; a fixture root carries the answer in a file instead.
func dpkgArch(root string) (string, error) {
	if root != "" {
		return readTrimmed(root + "/dpkg-architecture")
	}
	out, err := exec.Command("dpkg", "--print-architecture").Output()
	if err != nil {
		return "", fmt.Errorf("dpkg --print-architecture: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

func readTrimmed(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading %s: %w", path, err)
	}
	return strings.TrimSpace(string(b)), nil
}
