package selfupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// release serves a release: a "binary" that is a shell script printing its
// version, VERSION, SHA256SUMS over both, and SHA256SUMS.sig made with key
// (or none).
type release struct {
	files map[string][]byte
}

func newRelease(t *testing.T, version string, key ed25519.PrivateKey) *release {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in binary is a shell script")
	}
	r := &release{files: map[string][]byte{
		"patchpilot-agent-linux-test": []byte("#!/bin/sh\necho " + version + "\n"),
		"VERSION":                     []byte(version + "\n"),
	}}
	r.sign(key)
	return r
}

func (r *release) sign(key ed25519.PrivateKey) {
	sums := ""
	for _, name := range []string{"VERSION", "patchpilot-agent-linux-test"} {
		sum := sha256.Sum256(r.files[name])
		sums += fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), name)
	}
	r.files["SHA256SUMS"] = []byte(sums)
	if key != nil {
		r.files["SHA256SUMS.sig"] = []byte(base64.StdEncoding.EncodeToString(ed25519.Sign(key, []byte(sums))) + "\n")
	}
}

func (r *release) serve(t *testing.T) string {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if b, ok := r.files[filepath.Base(req.URL.Path)]; ok {
			w.Write(b)
			return
		}
		http.NotFound(w, req)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

func setup(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey, string) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	target := filepath.Join(t.TempDir(), "patchpilot-agent")
	if err := os.WriteFile(target, []byte("#!/bin/sh\necho 0.1.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return public, private, target
}

func updater(base, target string, key ed25519.PublicKey) *Updater {
	return &Updater{Base: base, Target: target, Binary: "patchpilot-agent-linux-test", Current: "0.1.0", Keys: []ed25519.PublicKey{key}}
}

func unchanged(t *testing.T, target string) {
	t.Helper()
	b, _ := os.ReadFile(target)
	if string(b) != "#!/bin/sh\necho 0.1.0\n" {
		t.Fatalf("the agent was replaced: %q", b)
	}
	if _, err := os.Stat(target + ".new"); !os.IsNotExist(err) {
		t.Fatal("a .new file was left behind")
	}
}

func TestASignedNewerReleaseReplacesTheAgent(t *testing.T) {
	public, private, target := setup(t)
	base := newRelease(t, "0.2.0", private).serve(t)
	if err := updater(base, target, public).Update(context.Background(), "0.2.0"); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(target); string(b) != "#!/bin/sh\necho 0.2.0\n" {
		t.Fatalf("got %q", b)
	}
}

func TestAReleaseWithoutASignatureIsRefused(t *testing.T) {
	public, _, target := setup(t)
	base := newRelease(t, "0.2.0", nil).serve(t)
	if err := updater(base, target, public).Update(context.Background(), "0.2.0"); err == nil {
		t.Fatal("accepted")
	}
	unchanged(t, target)
}

func TestAReleaseSignedWithAnotherKeyIsRefused(t *testing.T) {
	public, _, target := setup(t)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	base := newRelease(t, "0.2.0", other).serve(t)
	if err := updater(base, target, public).Update(context.Background(), "0.2.0"); err != ErrSignature {
		t.Fatalf("got %v", err)
	}
	unchanged(t, target)
}

func TestABinaryThatIsNotTheSignedOneIsRefused(t *testing.T) {
	public, private, target := setup(t)
	r := newRelease(t, "0.2.0", private)
	r.files["patchpilot-agent-linux-test"] = []byte("#!/bin/sh\necho 0.2.0\n# changed\n")
	if err := updater(r.serve(t), target, public).Update(context.Background(), "0.2.0"); err == nil {
		t.Fatal("accepted")
	}
	unchanged(t, target)
}

func TestAnOlderSignedReleaseCannotPassForANewerOne(t *testing.T) {
	public, private, target := setup(t)
	base := newRelease(t, "0.1.5", private).serve(t)
	if err := updater(base, target, public).Update(context.Background(), "0.2.0"); err == nil {
		t.Fatal("accepted")
	}
	unchanged(t, target)
}

func TestNothingOlderOrEqualIsInstalled(t *testing.T) {
	public, private, target := setup(t)
	base := newRelease(t, "0.1.0", private).serve(t)
	for _, want := range []string{"0.1.0", "0.0.9", "dev", ""} {
		if err := updater(base, target, public).Update(context.Background(), want); err == nil {
			t.Fatalf("%q accepted", want)
		}
	}
	unchanged(t, target)
}

func TestABinaryThatDoesNotRunHereIsRefused(t *testing.T) {
	public, private, target := setup(t)
	r := newRelease(t, "0.2.0", private)
	r.files["patchpilot-agent-linux-test"] = []byte("not an executable")
	r.sign(private)
	if err := updater(r.serve(t), target, public).Update(context.Background(), "0.2.0"); err == nil {
		t.Fatal("accepted")
	}
	unchanged(t, target)
}

func TestVersionsCompareAsNumbers(t *testing.T) {
	for _, c := range []struct {
		a, b  string
		newer bool
	}{
		{"0.1.10", "0.1.9", true}, {"1.0.0", "0.9.9", true}, {"v0.2.0", "0.1.0", true},
		{"0.1.0", "0.1.0", false}, {"0.1.0", "0.2.0", false}, {"0.2.0-dev", "0.1.0", false}, {"dev", "0.1.0", false},
	} {
		if got := Newer(c.a, c.b); got != c.newer {
			t.Errorf("Newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestWithoutAReleaseKeyNothingIsTrusted(t *testing.T) {
	_, private, _ := ed25519.GenerateKey(rand.Reader)
	r := newRelease(t, "0.2.0", private)
	if _, err := Verify(r.files["SHA256SUMS"], r.files["SHA256SUMS.sig"], []ed25519.PublicKey{}); err != ErrSignature {
		t.Fatalf("got %v", err)
	}
}
