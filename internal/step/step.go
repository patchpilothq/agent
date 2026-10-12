// Package step is the agent's side of the update step (PROTOCOL.md): it can
// ask for one of three modes by creating an empty file, and nothing else.
package step

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
