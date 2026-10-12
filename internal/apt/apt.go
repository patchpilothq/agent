// Package apt asks apt, without root, what an upgrade would install. It only
// ever simulates: every command here runs with -s.
package apt

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

// Update is one entry of a report's "updates" (PROTOCOL.md).
type Update struct {
	Name     string   `json:"name"`
	From     string   `json:"from"`
	To       string   `json:"to"`
	Origins  []string `json:"origins"`
	Security bool     `json:"security"`
	HeldBack bool     `json:"held_back"`
}

// Runner runs apt-get with the given arguments and returns its output.
type Runner func(ctx context.Context, args ...string) ([]byte, error)

// AptGet runs the machine's apt-get, in the C locale so its output parses.
func AptGet(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "apt-get", args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("apt-get %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// Every simulation skips apt's lock, which an unprivileged user cannot take.
var simulate = []string{"-s", "-o", "Debug::NoLocking=true"}

// Pending lists every update apt knows of for an installed package. The
// full list ignores holds and phasing so that it can name what they keep
// back; an update is held back when a plain upgrade — which respects both,
// and never installs or removes a package — would not install it.
func Pending(ctx context.Context, run Runner) ([]Update, error) {
	full, err := run(ctx, append(simulate,
		"-o", "APT::Ignore-Hold=true",
		"-o", "APT::Get::Always-Include-Phased-Updates=true",
		"dist-upgrade")...)
	if err != nil {
		return nil, err
	}
	plain, err := run(ctx, append(simulate, "upgrade")...)
	if err != nil {
		return nil, err
	}

	upgradable := map[string]bool{}
	for _, u := range parse(string(plain)) {
		upgradable[u.Name] = true
	}
	updates := parse(string(full))
	for i := range updates {
		updates[i].HeldBack = !upgradable[updates[i].Name]
	}
	sort.Slice(updates, func(i, j int) bool { return updates[i].Name < updates[j].Name })
	return updates, nil
}

// An Inst line of a simulation: the package, its installed version in
// brackets (absent for a package that would be newly installed), then the
// new version, the archives it comes from and the architecture.
//
//	Inst libssl3 [3.0.11-1~deb12u2] (3.0.13-1~deb12u1 Debian:12.5/stable, Debian-Security:12/stable-security [amd64])
var instLine = regexp.MustCompile(`^Inst (\S+) \[([^\]]+)\] \((\S+) (.*) \[[^\]]+\]\)`)

func parse(out string) []Update {
	updates := []Update{}
	seen := map[string]bool{}
	scanner := bufio.NewScanner(strings.NewReader(out))
	for scanner.Scan() {
		m := instLine.FindStringSubmatch(scanner.Text())
		if m == nil || seen[m[1]] {
			continue
		}
		seen[m[1]] = true
		origins := strings.Split(m[4], ", ")
		updates = append(updates, Update{
			Name: m[1], From: m[2], To: m[3],
			Origins:  origins,
			Security: anySecurity(origins),
		})
	}
	return updates
}

// A security archive names itself so: Debian-Security:12/stable-security,
// Ubuntu:22.04/jammy-security, UbuntuESM:22.04/jammy-infra-security.
func anySecurity(origins []string) bool {
	for _, o := range origins {
		if strings.Contains(strings.ToLower(o), "security") {
			return true
		}
	}
	return false
}
