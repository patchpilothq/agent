// Command patchpilot-agent runs on a Debian or Ubuntu server as its own
// unprivileged user, reports the machine's package state to PatchPilot and
// asks the update step (PROTOCOL.md) to install pending updates when told
// to.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/patchpilothq/agent/internal/client"
	"github.com/patchpilothq/agent/internal/report"
	runs "github.com/patchpilothq/agent/internal/run"
	"github.com/patchpilothq/agent/internal/selfupdate"
	"github.com/patchpilothq/agent/internal/step"
	"github.com/patchpilothq/agent/internal/system"
)

// version is set at build time: -ldflags "-X main.version=…".
var version = "dev"

// exitRemoved tells systemd not to restart the agent
// (RestartPreventExitStatus in the unit): its server is gone.
const exitRemoved = 78

const usage = `usage:
  patchpilot-agent --version
  patchpilot-agent enroll [--config FILE] [--credential FILE]   (token on stdin)
  patchpilot-agent run [--config FILE] [--credential FILE] [--requests DIR]`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "--version" {
		fmt.Fprintln(stdout, version)
		return 0
	}
	if len(args) == 0 || (args[0] != "enroll" && args[0] != "run") {
		fmt.Fprintln(stderr, usage)
		return 2
	}

	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "/etc/patchpilot/agent.conf", "")
	credentialPath := flags.String("credential", "/var/lib/patchpilot/credential", "")
	requestsDir := flags.String("requests", "/var/lib/patchpilot-update/requests", "")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	logger := log.New(stderr, "", 0)

	server, err := readServer(*configPath)
	if err != nil {
		logger.Print(err)
		return 1
	}

	if args[0] == "enroll" {
		return enroll(ctx, server, *credentialPath, stdin, logger)
	}
	return serve(ctx, server, *credentialPath, *requestsDir, logger)
}

func enroll(ctx context.Context, server, credentialPath string, stdin io.Reader, logger *log.Logger) int {
	token, err := bufio.NewReader(stdin).ReadString('\n')
	token = strings.TrimSpace(token)
	if token == "" {
		logger.Printf("enroll: no token on standard input (%v)", err)
		return 2
	}
	info, err := system.Read(system.Paths{})
	if err != nil {
		logger.Printf("enroll: %v", err)
		return 1
	}
	cred, err := client.Enroll(ctx, server, token, info.Hostname, info.MachineIDHash)
	if err != nil {
		logger.Printf("enroll: %v", err)
		return 1
	}
	if err := client.SaveCredential(credentialPath, cred); err != nil {
		logger.Print(err)
		return 1
	}
	logger.Printf("enrolled as %s", cred.ServerID)
	return 0
}

func serve(ctx context.Context, server, credentialPath, requestsDir string, logger *log.Logger) int {
	cred, err := client.LoadCredential(credentialPath)
	if err != nil {
		logger.Print(err)
		return 1
	}
	go refreshLists(ctx, requestsDir, logger)

	// An update to a newer signed release ends the agent; systemd starts the
	// new binary (PROTOCOL.md, "update_agent").
	ctx, restart := context.WithCancel(ctx)
	defer restart()
	var updated atomic.Bool

	runner := &runs.Runner{
		RequestsDir: requestsDir,
		OutputPath:  filepath.Join(step.RunsDir, "output"),
		StatePath:   filepath.Join(filepath.Dir(credentialPath), "run.json"),
		Log:         logger,
		Context:     ctx,
	}
	session := &client.Session{
		Server:     server,
		Credential: cred,
		Version:    version,
		Collect: func(ctx context.Context) (client.State, error) {
			r, err := report.Collect(ctx, report.Machine())
			return state{r}, err
		},
		Lists:       func() any { return report.ReadLists(report.Machine()) },
		Fingerprint: fingerprint,
		Log:         logger,
		OnMessage: func(_ context.Context, msg map[string]any) {
			if msg["type"] == "update_agent" {
				version, _ := msg["version"].(string)
				go func() {
					if runner.Active() {
						logger.Printf("not updating to %s during a run; the server asks again later", version)
						return
					}
					if err := selfUpdate(ctx, server, version); err != nil {
						logger.Printf("not updating to %s: %v", version, err)
						return
					}
					logger.Printf("updated to %s; restarting", version)
					updated.Store(true)
					restart()
				}()
				return
			}
			runner.Handle(msg)
		},
		OnConnect: func(context.Context) { runner.Resume() },
	}
	runner.Send = session.Send
	err = session.Run(ctx)
	switch {
	case updated.Load():
		return 0
	case errors.Is(err, client.ErrRemoved):
		logger.Print("this server was removed from PatchPilot; stopping. Reinstall to connect it again.")
		return exitRemoved
	case ctx.Err() != nil:
		return 0
	default:
		logger.Print(err)
		return 1
	}
}

// selfUpdate replaces the agent's own binary with the signed release
// version from the server.
func selfUpdate(ctx context.Context, server, want string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if self, err = filepath.EvalSymlinks(self); err != nil {
		return err
	}
	info, err := system.Read(system.Paths{})
	if err != nil {
		return err
	}
	u := &selfupdate.Updater{
		Base:    server + "/agent/download",
		Target:  self,
		Binary:  "patchpilot-agent-linux-" + info.Arch,
		Current: version,
	}
	return u.Update(ctx, want)
}

// refreshLists asks the update step to refresh the package lists at once
// when it never has, and whenever its latest refresh is an hour old, so
// that pending updates are current — a cloud image comes without lists —
// and the report says when they were refreshed (PROTOCOL.md).
func refreshLists(ctx context.Context, requestsDir string, logger *log.Logger) {
	for {
		if refreshDue(step.ReadLists(step.RunsDir), time.Now()) {
			if err := step.Request(requestsDir, step.Refresh); err != nil {
				logger.Print(err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(5 * time.Minute):
		}
	}
}

// refreshDue says whether the package lists need a refresh.
func refreshDue(lists step.Lists, now time.Time) bool {
	return lists.CheckedAt.IsZero() || now.Sub(lists.CheckedAt) > time.Hour
}

// state is a report as the session sends it: whole, or as the changes from
// the state the server acknowledged (PROTOCOL.md, "Only what changed").
type state struct{ report.Report }

func (s state) DeltaFrom(base client.State) any { return s.Report.DeltaFrom(base.(state).Report) }

// fingerprint changes when anything a report is read from may have changed.
func fingerprint() string {
	var b strings.Builder
	for _, path := range []string{
		"/var/lib/dpkg/status",
		"/var/lib/apt/lists",
		"/var/lib/apt/extended_states",
		"/var/run/reboot-required",
		"/var/log/apt/history.log",
		filepath.Join(step.RunsDir, "lists-refreshed"),
		filepath.Join(step.RunsDir, "lists-error"),
	} {
		if info, err := os.Stat(path); err == nil {
			fmt.Fprintf(&b, "%s %d %d;", path, info.ModTime().UnixNano(), info.Size())
		} else {
			fmt.Fprintf(&b, "%s -;", path)
		}
	}
	return b.String()
}

// readServer reads the server's address from the agent's configuration,
// which the install script writes and only root can change.
func readServer(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("reading the configuration: %w", err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok && strings.TrimSpace(key) == "server" {
			if value = strings.TrimSpace(value); strings.HasPrefix(value, "https://") || strings.HasPrefix(value, "http://") {
				return strings.TrimRight(value, "/"), nil
			}
		}
	}
	return "", fmt.Errorf("reading the configuration: %s names no server", path)
}
