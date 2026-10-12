// Package report assembles the "report" message (PROTOCOL.md) from what the
// other packages read. It is the complete list of what the agent sends.
package report

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"time"

	"github.com/patchpilothq/agent/internal/apt"
	"github.com/patchpilothq/agent/internal/packages"
	"github.com/patchpilothq/agent/internal/system"
)

// Report is the message.
type Report struct {
	Type          string             `json:"type"`
	System        system.Info        `json:"system"`
	Packages      []packages.Package `json:"packages"`
	Updates       []apt.Update       `json:"updates"`
	Held          []string           `json:"held"`
	Reboot        packages.Reboot    `json:"reboot"`
	LastUpgradeAt *time.Time         `json:"last_upgrade_at"`
	Pro           *Pro               `json:"pro,omitempty"`
}

// Pro says whether Ubuntu Pro is attached; sent on Ubuntu only.
type Pro struct {
	Attached bool `json:"attached"`
}

// Sources says where a report is read from. Root prefixes every file, for
// tests; Apt and ProStatus run the commands.
type Sources struct {
	Root      string
	Apt       apt.Runner
	ProStatus func(ctx context.Context) ([]byte, error)
}

// Machine reads the machine the agent runs on.
func Machine() Sources {
	return Sources{Apt: apt.AptGet, ProStatus: proStatus}
}

// Collect reads everything once.
func Collect(ctx context.Context, s Sources) (Report, error) {
	r := Report{Type: "report"}
	var err error

	if r.System, err = system.Read(system.Paths{Root: s.Root}); err != nil {
		return r, err
	}
	var sources map[string]string
	if r.Packages, r.Held, sources, err = packages.Installed(s.Root + "/var/lib/dpkg/status"); err != nil {
		return r, err
	}
	if r.Held == nil {
		r.Held = []string{}
	}
	if r.Updates, err = apt.Pending(ctx, s.Apt); err != nil {
		return r, err
	}
	// The source package names the update for security data, which is
	// published per source package (PROTOCOL.md).
	for i, u := range r.Updates {
		name, _, _ := strings.Cut(u.Name, ":")
		if source, ok := sources[name]; ok {
			r.Updates[i].Source = source
		} else {
			r.Updates[i].Source = name
		}
	}
	if r.Reboot, err = packages.RebootRequired(s.Root + "/var/run/reboot-required"); err != nil {
		return r, err
	}
	r.LastUpgradeAt = packages.LastUpgrade(s.Root + "/var/log/dpkg.log")
	if r.System.OSID == "ubuntu" && s.ProStatus != nil {
		r.Pro = &Pro{}
		// No Pro client, or one that cannot answer, is reported as not attached.
		if out, err := s.ProStatus(ctx); err == nil {
			var status struct {
				Attached bool `json:"attached"`
			}
			if json.Unmarshal(out, &status) == nil {
				r.Pro.Attached = status.Attached
			}
		}
	}
	return r, nil
}

func proStatus(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "pro", "status", "--format", "json").Output()
}
