// Package packages reads the machine's package state from dpkg's and apt's
// own files: what is installed, what is on hold, whether a reboot is
// waiting and when packages were last upgraded. Each file is readable by any
// user.
package packages

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

// Package is one installed package: name, version, architecture.
type Package [3]string

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
	var name, version, arch, status, source string
	flush := func() {
		fields := strings.Fields(status)
		if name != "" && len(fields) == 3 && fields[2] == "installed" {
			installed = append(installed, Package{name, version, arch})
			if fields[0] == "hold" {
				held = append(held, name)
			}
			if source != "" && source != name {
				sources[name] = source
			}
		}
		name, version, arch, status, source = "", "", "", "", ""
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
			// "glibc" or "glibc (2.36-9)": the version, when given, is the
			// source's own and not needed here.
			source = strings.Fields(value)[0]
		}
	}
	flush()
	if err := scanner.Err(); err != nil {
		return nil, nil, nil, fmt.Errorf("reading dpkg status: %w", err)
	}
	sort.Slice(installed, func(i, j int) bool {
		if installed[i][0] != installed[j][0] {
			return installed[i][0] < installed[j][0]
		}
		return installed[i][2] < installed[j][2]
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

	b, err := os.ReadFile(flagPath + ".pkgs")
	if os.IsNotExist(err) {
		return reboot, nil
	} else if err != nil {
		return reboot, fmt.Errorf("reading %s.pkgs: %w", flagPath, err)
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

// LastUpgrade is the start of the newest apt run in the history log that
// upgraded something, or nil. The current log is read first, then the
// rotated ones, newest first.
func LastUpgrade(historyPath string) (*time.Time, error) {
	for _, path := range []string{historyPath, historyPath + ".1", historyPath + ".1.gz", historyPath + ".2.gz"} {
		at, err := lastUpgradeIn(path)
		if err != nil {
			return nil, err
		}
		if at != nil {
			return at, nil
		}
	}
	return nil, nil
}

func lastUpgradeIn(path string) (*time.Time, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	defer f.Close()

	var r io.Reader = f
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", path, err)
		}
		defer gz.Close()
		r = gz
	}

	var start string
	var last *time.Time
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 1024*1024), 4*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "Start-Date:"):
			start = strings.TrimSpace(strings.TrimPrefix(line, "Start-Date:"))
		case strings.HasPrefix(line, "Upgrade:") && start != "":
			// apt writes local time without a zone.
			if at, err := time.ParseInLocation("2006-01-02  15:04:05", start, time.Local); err == nil {
				utc := at.UTC()
				last = &utc
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	return last, nil
}
