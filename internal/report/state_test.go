package report

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/patchpilothq/agent/internal/apt"
	"github.com/patchpilothq/agent/internal/packages"
)

// The test vector the server's tests check as well (PROTOCOL.md, "Only what
// changed"); its digest was computed from the protocol's text, not from
// either implementation.
func vector(t *testing.T) Report {
	t.Helper()
	b, err := os.ReadFile("../../testdata/digest/report.json")
	if err != nil {
		t.Fatal(err)
	}
	var r Report
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTheDigestIsTheProtocolsCanonicalText(t *testing.T) {
	r := vector(t)
	text, _ := os.ReadFile("../../testdata/digest/canonical.txt")
	digest, _ := os.ReadFile("../../testdata/digest/digest.txt")

	if got := string(Canonical(r)); got != string(text) {
		t.Fatalf("canonical text:\n%s\nwant:\n%s", got, text)
	}
	if got := r.Digest(); got != strings.TrimSpace(string(digest)) {
		t.Fatalf("digest %s, want %s", got, digest)
	}
}

func TestTheDigestDoesNotDependOnOrder(t *testing.T) {
	r := vector(t)
	shuffled := r
	shuffled.Packages = slices.Clone(r.Packages)
	slices.Reverse(shuffled.Packages)
	shuffled.Updates = slices.Clone(r.Updates)
	slices.Reverse(shuffled.Updates)
	if r.Digest() != shuffled.Digest() {
		t.Fatal("the same state in another order has another digest")
	}
}

func TestADeltaCarriesOnlyWhatChanged(t *testing.T) {
	base := vector(t)
	next := base
	next.Packages = []packages.Package{
		{"openssl", "3.0.15-1~deb12u1", "amd64"}, // upgraded
		{"libc6", "2.36-9", "i386"},
		{"libc6", "2.36-9", "amd64"},
		{"curl", "7.88.1-10", "amd64"}, // new; libssl3 is gone
	}
	next.Updates = []apt.Update{base.Updates[1]} // openssl's update is installed

	d := next.DeltaFrom(base)
	if d.Type != "report_delta" || d.Base != base.Digest() || d.Digest != next.Digest() {
		t.Fatalf("names %+v", d)
	}
	if got := d.Packages.Set; len(got) != 2 || got[0][0] != "openssl" || got[1][0] != "curl" {
		t.Fatalf("set %v", got)
	}
	if got := d.Packages.Removed; len(got) != 1 || got[0] != [2]string{"libssl3", "amd64"} {
		t.Fatalf("removed %v", got)
	}
	if len(d.Updates.Set) != 0 || !slices.Equal(d.Updates.Removed, []string{"openssl"}) {
		t.Fatalf("updates %+v", d.Updates)
	}
}

// What the server does with a delta, done here too: applied onto its base,
// it gives the state its digest names.
func TestADeltaAppliedOntoItsBaseGivesTheNewState(t *testing.T) {
	base := vector(t)
	next := base
	next.Packages = append(slices.Clone(base.Packages[1:]), packages.Package{"curl", "7.88.1-10", "amd64"})
	changed := base.Updates[0]
	changed.To = "3.0.16-1~deb12u1"
	next.Updates = []apt.Update{changed}
	next.Held = []string{}

	d := next.DeltaFrom(base)
	b, _ := json.Marshal(d)
	var wire Delta
	json.Unmarshal(b, &wire)

	applied := base
	keep := func(p packages.Package) bool {
		for _, r := range wire.Packages.Removed {
			if r == [2]string{p[0], p[2]} {
				return false
			}
		}
		for _, s := range wire.Packages.Set {
			if s[0] == p[0] && s[2] == p[2] {
				return false
			}
		}
		return true
	}
	applied.Packages = append(slices.DeleteFunc(slices.Clone(base.Packages), func(p packages.Package) bool { return !keep(p) }), wire.Packages.Set...)
	applied.Updates = slices.DeleteFunc(slices.Clone(base.Updates), func(u apt.Update) bool {
		return slices.Contains(wire.Updates.Removed, u.Name) || slices.ContainsFunc(wire.Updates.Set, func(s apt.Update) bool { return s.Name == u.Name })
	})
	applied.Updates = append(applied.Updates, wire.Updates.Set...)
	applied.Held, applied.Reboot, applied.System = wire.Held, wire.Reboot, wire.System

	if applied.Digest() != wire.Digest {
		t.Fatalf("applied digest %s, want %s", applied.Digest(), wire.Digest)
	}
}

func TestAnUnchangedStateHasAnEmptyDelta(t *testing.T) {
	r := vector(t)
	d := r.DeltaFrom(r)
	if d.Base != d.Digest || len(d.Packages.Set)+len(d.Packages.Removed)+len(d.Updates.Set)+len(d.Updates.Removed) != 0 {
		t.Fatalf("delta %+v", d)
	}
}
