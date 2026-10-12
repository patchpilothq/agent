// Package step is the agent's side of the update step (PROTOCOL.md): it can
// ask for one of three modes by creating an empty file, and nothing else.
package step

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// The modes the update step accepts, and the one request it reads while it
// installs: stop after the package in progress. The step itself enforces
// the list; the agent only never asks for anything else.
const (
	Refresh  = "refresh"
	Security = "security"
	Upgrade  = "upgrade"
	Stop     = "stop"
)

// RunsDir is where the step writes, readable by the agent and writable only
// by root.
const RunsDir = "/var/lib/patchpilot-update/runs"

// Request asks the step for mode. A request already waiting is left alone.
func Request(requestsDir, mode string) error {
	switch mode {
	case Refresh, Security, Upgrade, Stop:
	default:
		return fmt.Errorf("no such mode: %q", mode)
	}
	f, err := os.OpenFile(filepath.Join(requestsDir, mode), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if errors.Is(err, os.ErrExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("asking the update step to %s: %w", mode, err)
	}
	return f.Close()
}

// Lists is what the update step recorded about the package lists
// (PROTOCOL.md, "The update step").
type Lists struct {
	// RefreshedAt is when a refresh last succeeded; nil if none has.
	RefreshedAt *time.Time
	// Error is apt's lines from the latest refresh, when it failed.
	Error string
	// CheckedAt is when the latest refresh ended, failed or not; zero if
	// there was none.
	CheckedAt time.Time
}

// maxListsError bounds what is read of apt's lines; the step writes ten.
const maxListsError = 2000

// ReadLists reads what the step recorded in runsDir. A file that is missing
// or unreadable counts as not written.
func ReadLists(runsDir string) Lists {
	var l Lists
	if b, info, ok := read(filepath.Join(runsDir, "lists-refreshed")); ok {
		if t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b))); err == nil {
			l.RefreshedAt = &t
		}
		l.CheckedAt = info.ModTime()
	}
	if b, info, ok := read(filepath.Join(runsDir, "lists-error")); ok {
		l.Error = strings.TrimSpace(strings.ToValidUTF8(string(b), "?"))
		if info.ModTime().After(l.CheckedAt) {
			l.CheckedAt = info.ModTime()
		}
	}
	return l
}

func read(path string) ([]byte, os.FileInfo, bool) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, nil, false
	}
	b, err := io.ReadAll(io.LimitReader(f, maxListsError))
	if err != nil {
		return nil, nil, false
	}
	return b, info, true
}
