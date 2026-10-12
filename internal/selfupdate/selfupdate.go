// Package selfupdate replaces the agent's own binary with a newer release,
// and only with one signed by PatchPilot's release key. It never touches
// the update step, which runs as root and changes only when the machine's
// owner reinstalls.
//
// A release is a directory of files whose SHA-256 sums are listed in
// SHA256SUMS; SHA256SUMS.sig is the Ed25519 signature of that list, made
// by the release pipeline. The list also covers VERSION, so a signed older
// release cannot be passed off as a newer one.
package selfupdate

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// releaseKeys are the public keys a release may be signed with: the
// release pipeline's, and during a rotation the next one too. Anyone can
// check a release against them.
var releaseKeys = []string{}

// devKey is set at build time, for development builds only:
// -ldflags "-X github.com/patchpilothq/agent/internal/selfupdate.devKey=…".
// Release builds never set it.
var devKey string

func keys() []ed25519.PublicKey {
	var out []ed25519.PublicKey
	for _, k := range append(append([]string{}, releaseKeys...), devKey) {
		if b, err := base64.StdEncoding.DecodeString(k); err == nil && len(b) == ed25519.PublicKeySize {
			out = append(out, ed25519.PublicKey(b))
		}
	}
	return out
}

// ErrSignature means the release is not signed with a release key.
var ErrSignature = errors.New("the release is not signed with PatchPilot's release key")

// Verify checks the signature over SHA256SUMS against keys (nil: the
// compiled-in ones) and returns the listed sums by file name.
func Verify(sums, sig []byte, with []ed25519.PublicKey) (map[string]string, error) {
	if with == nil {
		with = keys()
	}
	signature, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil || len(signature) != ed25519.SignatureSize {
		return nil, ErrSignature
	}
	ok := false
	for _, key := range with {
		if ed25519.Verify(key, sums, signature) {
			ok = true
			break
		}
	}
	if !ok {
		return nil, ErrSignature
	}
	listed := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(sums))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) == 2 {
			listed[strings.TrimPrefix(fields[1], "*")] = fields[0]
		}
	}
	return listed, nil
}

// Newer reports whether version a is newer than b. Both are dotted numbers,
// such as 0.2.10; anything else is never newer, so a development build
// neither updates nor is updated to.
func Newer(a, b string) bool {
	pa, okA := parse(a)
	pb, okB := parse(b)
	if !okA || !okB {
		return false
	}
	for i := 0; i < 3; i++ {
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return false
}

func parse(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}

// Updater replaces Target with the release at Base.
type Updater struct {
	Base    string // e.g. https://patchpilot.cc/agent/download
	Target  string // the agent's own binary
	Binary  string // its release file name, e.g. patchpilot-agent-linux-arm64
	Current string // the running version
	Keys    []ed25519.PublicKey
	Client  *http.Client
}

// Update installs version want if it is newer than the running one and
// properly signed. On success the caller exits, and systemd starts the new
// binary.
func (u *Updater) Update(ctx context.Context, want string) error {
	if !Newer(want, u.Current) {
		return fmt.Errorf("%s is not newer than %s", want, u.Current)
	}
	sums, err := u.get(ctx, "SHA256SUMS", 64*1024)
	if err != nil {
		return err
	}
	sig, err := u.get(ctx, "SHA256SUMS.sig", 4*1024)
	if err != nil {
		return err
	}
	listed, err := Verify(sums, sig, u.Keys)
	if err != nil {
		return err
	}
	version, err := u.get(ctx, "VERSION", 1024)
	if err != nil {
		return err
	}
	if err := matches(listed, "VERSION", version); err != nil {
		return err
	}
	if got := strings.TrimSpace(string(version)); got != want {
		return fmt.Errorf("the release is %s, not %s", got, want)
	}
	binary, err := u.get(ctx, u.Binary, 64*1024*1024)
	if err != nil {
		return err
	}
	if err := matches(listed, u.Binary, binary); err != nil {
		return err
	}

	next := u.Target + ".new"
	if err := os.WriteFile(next, binary, 0o755); err != nil {
		return fmt.Errorf("writing the new agent: %w", err)
	}
	// The service's umask would leave it executable by its user only; the
	// machine's admins run --version too.
	if err := os.Chmod(next, 0o755); err != nil {
		return fmt.Errorf("writing the new agent: %w", err)
	}
	// The new binary must run here before it replaces the one that does.
	out, err := exec.CommandContext(ctx, next, "--version").Output()
	if err != nil || strings.TrimSpace(string(out)) != want {
		os.Remove(next)
		return fmt.Errorf("the new agent does not run here (%v, %q)", err, out)
	}
	if err := os.Rename(next, u.Target); err != nil {
		os.Remove(next)
		return fmt.Errorf("replacing the agent: %w", err)
	}
	return nil
}

func matches(listed map[string]string, name string, content []byte) error {
	sum := sha256.Sum256(content)
	if want, ok := listed[name]; !ok || want != hex.EncodeToString(sum[:]) {
		return fmt.Errorf("%s does not match the signed SHA256SUMS", name)
	}
	return nil
}

func (u *Updater) get(ctx context.Context, name string, limit int64) ([]byte, error) {
	client := u.Client
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(u.Base, "/")+"/"+name, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: %s", name, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", name, err)
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("downloading %s: larger than expected", name)
	}
	return b, nil
}
