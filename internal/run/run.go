// Package run carries out a run the server asked for (PROTOCOL.md, "Runs"):
// it asks the update step to install, follows the step's output, and turns
// it into progress and a result. A run belongs to the machine, not to the
// connection: it goes on when the connection drops, and its result is kept
// on disk until it was sent.
package run

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/patchpilothq/agent/internal/step"
)

// Progress is a "run_progress" message.
type Progress struct {
	Type    string `json:"type"`
	RunID   string `json:"run_id"`
	State   string `json:"state"` // "refreshing", "waiting" (for dpkg's lock) or "installing"
	Done    int    `json:"done"`
	Total   int    `json:"total"`
	Package string `json:"package,omitempty"`
}

// Failure is one package apt refused, with what it said.
type Failure struct {
	Package string `json:"package"`
	Output  string `json:"output"`
}

// Finished is a "run_finished" message.
type Finished struct {
	Type      string    `json:"type"`
	RunID     string    `json:"run_id"`
	Result    string    `json:"result"` // "succeeded", "failed" or "stopped"
	Installed []string  `json:"installed"`
	Failed    []Failure `json:"failed"`
	Output    string    `json:"output"` // the end of the step's output
}

// Runner runs one run at a time.
type Runner struct {
	RequestsDir string // the update step's requests directory
	OutputPath  string // the update step's output
	StatePath   string // where a run in progress or an unsent result is kept
	Send        func(any) error
	Log         *log.Logger
	// StepActive says whether the update step's service is running; nil
	// asks systemctl.
	StepActive func() bool
	// Poll is how often the output is read; zero is one second.
	Poll time.Duration
	// Context bounds the runs: the agent's own, not a connection's.
	Context context.Context

	mu     sync.Mutex
	active bool
}

type state struct {
	RunID       string    `json:"run_id"`
	Mode        string    `json:"mode"`
	RequestedAt time.Time `json:"requested_at"`
	Finished    *Finished `json:"finished,omitempty"`
}

// Active reports whether a run is going on.
func (r *Runner) Active() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.active
}

// Handle takes the server's "run" and "stop" messages.
func (r *Runner) Handle(msg map[string]any) {
	id, _ := msg["run_id"].(string)
	switch msg["type"] {
	case "run":
		mode, _ := msg["mode"].(string)
		if id == "" || (mode != step.Upgrade && mode != step.Security) {
			return
		}
		r.start(state{RunID: id, Mode: mode, RequestedAt: time.Now()}, true)
	case "stop":
		r.mu.Lock()
		active := r.active
		r.mu.Unlock()
		if active {
			if err := step.Request(r.RequestsDir, step.Stop); err != nil {
				r.Log.Print(err)
			}
		}
	}
}

// Resume goes on with a run the agent was following when it stopped, and
// sends a result it could not send then. Called at start and on every new
// connection.
func (r *Runner) Resume() {
	s, err := r.load()
	if err != nil {
		return
	}
	if s.Finished != nil {
		if r.Send(*s.Finished) == nil {
			os.Remove(r.StatePath)
		}
		return
	}
	r.start(s, false)
}

func (r *Runner) start(s state, request bool) {
	r.mu.Lock()
	if r.active {
		r.mu.Unlock()
		return
	}
	r.active = true
	r.mu.Unlock()

	if err := r.save(s); err != nil {
		r.Log.Print(err)
	}
	if request {
		if err := step.Request(r.RequestsDir, s.Mode); err != nil {
			r.Log.Print(err)
		}
	}

	go func() {
		defer func() {
			r.mu.Lock()
			r.active = false
			r.mu.Unlock()
		}()
		finished := r.follow(r.Context, s)
		s.Finished = &finished
		if r.Send(finished) == nil {
			os.Remove(r.StatePath)
		} else if err := r.save(s); err != nil {
			r.Log.Print(err)
		}
	}()
}

// follow reads the step's output until it says it is done.
func (r *Runner) follow(ctx context.Context, s state) Finished {
	poll := r.Poll
	if poll == 0 {
		poll = time.Second
	}
	finished := Finished{Type: "run_finished", RunID: s.RunID, Installed: []string{}, Failed: []Failure{}}
	p := &parser{finished: &finished, send: r.Send, runID: s.RunID}

	var offset int64
	started := false
	quietSince := time.Now()
	for {
		select {
		case <-ctx.Done():
			finished.Result = "failed"
			return finished
		case <-time.After(poll):
		}

		// Until this run's start line is there, the whole file is read
		// again each time: the step truncates it when it begins, and the new
		// output may already be longer than the old by the next look.
		if !started {
			offset = 0
		}
		lines, next, err := readFrom(r.OutputPath, offset)
		if err == nil && len(lines) > 0 {
			quietSince = time.Now()
		}
		offset = next

		for _, line := range lines {
			if !started {
				// The step's first line names the mode and when it began; an
				// older run's output is not this run's.
				if f := strings.Fields(line); len(f) == 4 && f[0] == "PATCHPILOT" && f[1] == "start" && f[2] == s.Mode {
					if at, err := time.Parse(time.RFC3339, f[3]); err == nil && !at.Before(s.RequestedAt.Add(-5*time.Second)) {
						started = true
						p.send(Progress{Type: "run_progress", RunID: s.RunID, State: "refreshing"})
					}
				}
				continue
			}
			if p.line(line) {
				return finished
			}
		}

		switch {
		case !started && time.Since(s.RequestedAt) > 3*time.Minute && !r.stepActive():
			finished.Result = "failed"
			finished.Output = "The update step did not start."
			return finished
		case started && time.Since(quietSince) > 30*time.Minute && !r.stepActive():
			finished.Result = "failed"
			finished.Output = p.tail() + "\nThe update step stopped without finishing."
			return finished
		}
	}
}

func (r *Runner) stepActive() bool {
	if r.StepActive != nil {
		return r.StepActive()
	}
	return exec.Command("systemctl", "is-active", "--quiet", "patchpilot-update.service").Run() == nil
}

// parser turns the step's lines into progress and the result.
type parser struct {
	finished *Finished
	send     func(any) error
	runID    string

	current string   // the package being installed
	failed  bool     // whether it failed
	since   []string // apt's lines since it began
	all     []string // the last lines of everything
	done    int
	total   int
}

// line handles one line; true when the run is over.
func (p *parser) line(line string) bool {
	p.all = append(p.all, line)
	if len(p.all) > 60 {
		p.all = p.all[len(p.all)-60:]
	}
	f := strings.Fields(line)
	if len(f) < 2 || f[0] != "PATCHPILOT" {
		p.since = append(p.since, line)
		return false
	}
	switch f[1] {
	case "wait":
		p.send(Progress{Type: "run_progress", RunID: p.runID, State: "waiting", Done: p.done, Total: p.total, Package: p.current})
	case "package":
		if len(f) == 5 {
			p.closePackage()
			p.done, _ = strconv.Atoi(f[2])
			p.done--
			p.total, _ = strconv.Atoi(f[3])
			p.current, p.failed, p.since = f[4], false, nil
			p.send(Progress{Type: "run_progress", RunID: p.runID, State: "installing", Done: p.done, Total: p.total, Package: p.current})
		}
	case "failed":
		if len(f) >= 3 {
			p.failed = true
			out := p.since
			if len(out) > 30 {
				out = out[len(out)-30:]
			}
			p.finished.Failed = append(p.finished.Failed, Failure{Package: f[2], Output: strings.Join(out, "\n")})
		}
	case "broken":
		// apt's dependencies are broken after a failure; the step stopped.
		p.all = append(p.all, "apt's dependencies are broken after this failure; PatchPilot stopped the run rather than remove a package. Mend it on the server, for example with: sudo apt --fix-broken install")
	case "stopped":
		p.finished.Result = "stopped"
	case "done":
		p.closePackage()
		code := 0
		if len(f) >= 3 {
			code, _ = strconv.Atoi(f[2])
		}
		switch {
		case p.finished.Result == "stopped":
		case code != 0 || len(p.finished.Failed) > 0:
			p.finished.Result = "failed"
		default:
			p.finished.Result = "succeeded"
		}
		p.finished.Output = p.tail()
		return true
	}
	return false
}

func (p *parser) closePackage() {
	if p.current != "" && !p.failed {
		p.finished.Installed = append(p.finished.Installed, p.current)
	}
	p.current = ""
}

func (p *parser) tail() string { return strings.Join(p.all, "\n") }

// readFrom reads the complete lines after offset, and where they end.
func readFrom(path string, offset int64) ([]string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, offset, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, offset, err
	}
	if info.Size() < offset {
		return nil, info.Size(), nil
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, offset, err
	}
	var lines []string
	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadString('\n')
		if errors.Is(err, io.EOF) {
			break // an incomplete line waits for its end
		}
		if err != nil {
			return lines, offset, err
		}
		offset += int64(len(line))
		lines = append(lines, strings.TrimRight(line, "\n"))
	}
	return lines, offset, nil
}

func (r *Runner) load() (state, error) {
	var s state
	b, err := os.ReadFile(r.StatePath)
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil || s.RunID == "" {
		return s, fmt.Errorf("%s is not a run", r.StatePath)
	}
	return s, nil
}

func (r *Runner) save(s state) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	tmp := r.StatePath + ".new"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("keeping the run: %w", err)
	}
	return os.Rename(tmp, r.StatePath)
}
