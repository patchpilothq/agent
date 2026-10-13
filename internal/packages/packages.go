// Package packages reads the machine's package state from dpkg's and apt's
// own files: what is installed, what is on hold, whether a reboot is
// waiting and when packages were last upgraded. Each file is readable by any
// user.
package packages

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

// Package is one installed package and the source package it was built
// from, which security data is published under (PROTOCOL.md). In a report
// it is [name, version, architecture], followed by the source's name when
// that differs from the package's, and by the source's version when that
// differs from the package's.
type Package struct {
	Name, Version, Arch   string
	Source, SourceVersion string // empty: the package's own
}

// SourceName is the source package's name.
func (p Package) SourceName() string {
	if p.Source == "" {
		return p.Name
	}
	return p.Source
}

// SourceVer is the source package's version.
func (p Package) SourceVer() string {
	if p.SourceVersion == "" {
		return p.Version
	}
	return p.SourceVersion
}

// MarshalJSON writes the package as the protocol's array.
func (p Package) MarshalJSON() ([]byte, error) {
	fields := []string{p.Name, p.Version, p.Arch}
	switch {
	case p.SourceVersion != "" && p.SourceVersion != p.Version:
		fields = append(fields, p.SourceName(), p.SourceVersion)
	case p.Source != "" && p.Source != p.Name:
		fields = append(fields, p.Source)
	}
	return json.Marshal(fields)
}

// UnmarshalJSON reads the protocol's array of three to five strings.
func (p *Package) UnmarshalJSON(b []byte) error {
	var fields []string
	if err := json.Unmarshal(b, &fields); err != nil {
		return err
	}
	if len(fields) < 3 || len(fields) > 5 {
		return fmt.Errorf("a package has three to five fields, not %d", len(fields))
	}
	*p = Package{Name: fields[0], Version: fields[1], Arch: fields[2]}
	if len(fields) > 3 {
		p.Source = fields[3]
	}
	if len(fields) > 4 {
		p.SourceVersion = fields[4]
	}
	return nil
}

// Installed reads dpkg's status file: every package whose status is
// "install ok installed", the names of those put on hold, and the source
// package each one was built from where it differs from its own name.
func Installed(statusPath string) (installed []Package, held []string, sources map[string]string, err error) {
	f, err := os.Open(statusPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("reading dpkg status: %w", err)
	}
	defer f.Close()
	sources = map[string]string{}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	var name, version, arch, status, source, sourceVersion string
	flush := func() {
		fields := strings.Fields(status)
		if name != "" && len(fields) == 3 && fields[2] == "installed" {
			p := Package{Name: name, Version: version, Arch: arch}
			if source != name {
				p.Source = source
			}
			if sourceVersion != version {
				p.SourceVersion = sourceVersion
			}
			installed = append(installed, p)
			if fields[0] == "hold" {
				held = append(held, name)
			}
			if source != "" && source != name {
				sources[name] = source
			}
		}
		name, version, arch, status, source, sourceVersion = "", "", "", "", "", ""
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			flush()
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			continue // a continuation line of a long field
		}
		key, value, _ := strings.Cut(line, ":")
		value = strings.TrimSpace(value)
		switch key {
		case "Package":
			name = value
		case "Version":
			version = value
		case "Architecture":
			arch = value
		case "Status":
			status = value
		case "Source":
			// "glibc", or "glibc (2.36-9)" when the source's version is not
			// the package's own.
			fields := strings.Fields(value)
			source = fields[0]
			if len(fields) > 1 {
				sourceVersion = strings.Trim(fields[1], "()")
			}
		}
	}
	flush()
	if err := scanner.Err(); err != nil {
		return nil, nil, nil, fmt.Errorf("reading dpkg status: %w", err)
	}
	sort.Slice(installed, func(i, j int) bool {
		if installed[i].Name != installed[j].Name {
			return installed[i].Name < installed[j].Name
		}
		return installed[i].Arch < installed[j].Arch
	})
	sort.Strings(held)
	return installed, held, sources, nil
}

// Reboot is the "reboot" object of a report.
type Reboot struct {
	Required bool     `json:"required"`
	Packages []string `json:"packages"`
}

// RebootRequired reads the flag Debian's and Ubuntu's packages set when
// they need a restart, and the list of packages that set it.
func RebootRequired(flagPath string) (Reboot, error) {
	reboot := Reboot{Packages: []string{}}
	if _, err := os.Stat(flagPath); os.IsNotExist(err) {
		return reboot, nil
	} else if err != nil {
		return reboot, fmt.Errorf("reading %s: %w", flagPath, err)
	}
	reboot.Required = true

	// Which packages asked for it is extra; a list it cannot read is empty.
	b, err := os.ReadFile(flagPath + ".pkgs")
	if err != nil {
		return reboot, nil
	}
	seen := map[string]bool{}
	for _, line := range strings.Split(string(b), "\n") {
		if name := strings.TrimSpace(line); name != "" && !seen[name] {
			seen[name] = true
			reboot.Packages = append(reboot.Packages, name)
		}
	}
	sort.Strings(reboot.Packages)
	return reboot, nil
}

// LastUpgrade is when a package was last upgraded, from dpkg's log — which
// every user can read, unlike apt's history — or nil. The current log is
// read first, then the rotated ones, newest first. A log it cannot read
// counts as none: when the last upgrade was is optional in a report.
func LastUpgrade(logPath string) *time.Time {
	for _, path := range []string{logPath, logPath + ".1", logPath + ".2.gz", logPath + ".3.gz"} {
		if at := lastUpgradeIn(path); at != nil {
			return at
		}
	}
	return nil
}

func lastUpgradeIn(path string) *time.Time {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil
		}
		defer gz.Close()
		r = gz
	}

	// 2026-10-12 13:25:31 upgrade openssl:arm64 3.0.15-1~deb12u1 3.0.17-1~deb12u1
	var last *time.Time
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 3 || fields[2] != "upgrade" {
			continue
		}
		// dpkg writes local time without a zone.
		if at, err := time.ParseInLocation("2006-01-02 15:04:05", fields[0]+" "+fields[1], time.Local); err == nil {
			utc := at.UTC()
			last = &utc
		}
	}
	return last
}
