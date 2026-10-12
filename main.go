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
	"syscall"
	"time"

	"github.com/patchpilothq/agent/internal/client"
	"github.com/patchpilothq/agent/internal/report"
	runs "github.com/patchpilothq/agent/internal/run"
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

	runner := &runs.Runner{
		RequestsDir: requestsDir,
		OutputPath:  "/var/lib/patchpilot-update/runs/output",
		StatePath:   filepath.Join(filepath.Dir(credentialPath), "run.json"),
		Log:         logger,
		Context:     ctx,
	}
	session := &client.Session{
		Server:      server,
		Credential:  cred,
		Version:     version,
		Collect:     func(ctx context.Context) (any, error) { return report.Collect(ctx, report.Machine()) },
		Fingerprint: fingerprint,
		Log:         logger,
		OnMessage:   func(_ context.Context, msg map[string]any) { runner.Handle(msg) },
		OnConnect:   func(context.Context) { runner.Resume() },
	}
	runner.Send = session.Send
	err = session.Run(ctx)
	switch {
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

// refreshLists asks the update step to refresh the package lists whenever
// they are older than six hours, so that pending updates are current even on
// a machine that does not refresh them itself.
func refreshLists(ctx context.Context, requestsDir string, logger *log.Logger) {
	const maxAge = 6 * time.Hour
	timer := time.NewTimer(time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		if info, err := os.Stat("/var/lib/apt/lists"); err == nil && time.Since(info.ModTime()) > maxAge {
			if err := step.Request(requestsDir, step.Refresh); err != nil {
				logger.Print(err)
			}
		}
		timer.Reset(30 * time.Minute)
	}
}

// fingerprint changes when anything a report is read from may have changed.
func fingerprint() string {
	var b strings.Builder
	for _, path := range []string{
		"/var/lib/dpkg/status",
		"/var/lib/apt/lists",
		"/var/lib/apt/extended_states",
		"/var/run/reboot-required",
		"/var/log/apt/history.log",
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
