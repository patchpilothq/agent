package report

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/patchpilothq/agent/internal/apt"
	"github.com/patchpilothq/agent/internal/packages"
	"github.com/patchpilothq/agent/internal/system"
)

// Canonical is a report's canonical text (PROTOCOL.md, "Only what changed"):
// one tab-separated row per item, sorted by its bytes. The server builds
// the same text from what it holds, so the two can name a state by its
// digest.
func Canonical(r Report) []byte {
	clean := strings.NewReplacer("\t", " ", "\n", " ", "\r", " ")
	row := func(fields ...string) string {
		for i, f := range fields {
			fields[i] = clean.Replace(f)
		}
		return strings.Join(fields, "\t") + "\n"
	}
	flag := func(b bool) string {
		if b {
			return "1"
		}
		return "0"
	}

	s := r.System
	rows := []string{row("system", s.Hostname, s.OSID, s.OSVersionID, s.OSCodename, s.OSName, s.OSVariant, s.Arch, s.Kernel)}
	for _, p := range r.Packages {
		rows = append(rows, row("package", p.Name, p.Version, p.Arch, p.SourceName(), p.SourceVer()))
	}
	for _, u := range r.Updates {
		source := u.Source
		if source == "" {
			source = u.Name
		}
		origins := slices.Clone(u.Origins)
		sort.Strings(origins)
		rows = append(rows, row("update", u.Name, source, u.From, u.To, strings.Join(origins, ","), flag(u.Security), flag(u.HeldBack)))
	}
	for _, h := range r.Held {
		rows = append(rows, row("held", h))
	}
	rebooting := slices.Clone(r.Reboot.Packages)
	sort.Strings(rebooting)
	rows = append(rows, row("reboot", flag(r.Reboot.Required), strings.Join(rebooting, ",")))
	last := ""
	if r.LastUpgradeAt != nil {
		last = r.LastUpgradeAt.UTC().Format(time.RFC3339)
	}
	rows = append(rows, row("last_upgrade_at", last))
	pro := ""
	if r.Pro != nil {
		pro = flag(r.Pro.Attached)
	}
	rows = append(rows, row("pro", pro))

	sort.Strings(rows)
	return []byte(strings.Join(rows, ""))
}

// Digest names the report's state: the SHA-256 of its canonical text.
func (r Report) Digest() string {
	sum := sha256.Sum256(Canonical(r))
	return hex.EncodeToString(sum[:])
}

// Full is the report as the "report" message.
func (r Report) Full() any { return r }

// Delta is the "report_delta" message: the changes from one state to
// another, the small parts whole.
type Delta struct {
	Type          string          `json:"type"`
	Base          string          `json:"base"`
	Digest        string          `json:"digest"`
	System        system.Info     `json:"system"`
	Held          []string        `json:"held"`
	Reboot        packages.Reboot `json:"reboot"`
	LastUpgradeAt *time.Time      `json:"last_upgrade_at"`
	Pro           *Pro            `json:"pro,omitempty"`
	Packages      PackageChanges  `json:"packages"`
	Updates       UpdateChanges   `json:"updates"`
}

// PackageChanges adds or replaces packages by name and architecture, and
// names the ones gone as [name, architecture].
type PackageChanges struct {
	Set     []packages.Package `json:"set"`
	Removed [][2]string        `json:"removed"`
}

// UpdateChanges adds or replaces updates by name, and names the ones gone.
type UpdateChanges struct {
	Set     []apt.Update `json:"set"`
	Removed []string     `json:"removed"`
}

// DeltaFrom is what changed from base to r.
func (r Report) DeltaFrom(base Report) Delta {
	d := Delta{
		Type: "report_delta", Base: base.Digest(), Digest: r.Digest(),
		System: r.System, Held: r.Held, Reboot: r.Reboot, LastUpgradeAt: r.LastUpgradeAt, Pro: r.Pro,
		Packages: PackageChanges{Set: []packages.Package{}, Removed: [][2]string{}},
		Updates:  UpdateChanges{Set: []apt.Update{}, Removed: []string{}},
	}

	before := map[[2]string]packages.Package{}
	for _, p := range base.Packages {
		before[[2]string{p.Name, p.Arch}] = p
	}
	now := map[[2]string]bool{}
	for _, p := range r.Packages {
		key := [2]string{p.Name, p.Arch}
		now[key] = true
		if old, ok := before[key]; !ok || old.Version != p.Version || old.SourceName() != p.SourceName() || old.SourceVer() != p.SourceVer() {
			d.Packages.Set = append(d.Packages.Set, p)
		}
	}
	for _, p := range base.Packages {
		if key := [2]string{p.Name, p.Arch}; !now[key] {
			d.Packages.Removed = append(d.Packages.Removed, key)
		}
	}

	was := map[string]apt.Update{}
	for _, u := range base.Updates {
		was[u.Name] = u
	}
	is := map[string]bool{}
	for _, u := range r.Updates {
		is[u.Name] = true
		if old, ok := was[u.Name]; !ok || !sameUpdate(old, u) {
			d.Updates.Set = append(d.Updates.Set, u)
		}
	}
	for _, u := range base.Updates {
		if !is[u.Name] {
			d.Updates.Removed = append(d.Updates.Removed, u.Name)
		}
	}
	return d
}

func sameUpdate(a, b apt.Update) bool {
	return a.Name == b.Name && a.Source == b.Source && a.From == b.From && a.To == b.To &&
		slices.Equal(a.Origins, b.Origins) && a.Security == b.Security && a.HeldBack == b.HeldBack
}
