// Command release makes and publishes a signed agent release. It is not
// part of the agent: the release pipeline and the key's keeper run it.
//
//	go run ./tools/release keygen -out signing.key   (once; prints the public key; make keygen)
//	go run ./tools/release sign -dir dist            (PATCHPILOT_SIGNING_KEY in the environment)
//	go run ./tools/release verify -dir dist -key <public key>
//	go run ./tools/release publish -dir dist -repo patchpilothq/agent -tag v0.2.0 -commit <sha>
//	                                                 (GITHUB_TOKEN in the environment)
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/patchpilothq/agent/internal/selfupdate"
)

func main() {
	if len(os.Args) < 2 {
		fail("usage: release keygen|sign|verify|publish …")
	}
	flags := flag.NewFlagSet(os.Args[1], flag.ExitOnError)
	dir := flags.String("dir", "dist", "the release directory")
	out := flags.String("out", "signing.key", "where keygen writes the private key")
	key := flags.String("key", "", "a public key, base64")
	repo := flags.String("repo", "patchpilothq/agent", "")
	tag := flags.String("tag", "", "")
	commit := flags.String("commit", "", "")
	flags.Parse(os.Args[2:])

	switch os.Args[1] {
	case "keygen":
		keygen(*out)
	case "sign":
		sign(*dir)
	case "verify":
		verify(*dir, *key)
	case "publish":
		publish(*dir, *repo, *tag, *commit)
	default:
		fail("unknown command " + os.Args[1])
	}
}

func keygen(out string) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	check(err)
	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	check(err)
	_, err = fmt.Fprintln(f, base64.StdEncoding.EncodeToString(private.Seed()))
	check(err)
	check(f.Close())
	fmt.Printf("private key: %s (keep it offline, and as the release pipeline's secret)\n", out)
	fmt.Printf("public key:  %s\n", base64.StdEncoding.EncodeToString(public))
}

func sign(dir string) {
	seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("PATCHPILOT_SIGNING_KEY")))
	if err != nil || len(seed) != ed25519.SeedSize {
		fail("PATCHPILOT_SIGNING_KEY is not a signing key")
	}
	sums, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	check(err)
	signature := ed25519.Sign(ed25519.NewKeyFromSeed(seed), sums)
	check(os.WriteFile(filepath.Join(dir, "SHA256SUMS.sig"), []byte(base64.StdEncoding.EncodeToString(signature)+"\n"), 0o644))
	fmt.Println("signed", filepath.Join(dir, "SHA256SUMS"))
}

func verify(dir, key string) {
	public, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(public) != ed25519.PublicKeySize {
		fail("-key is not a public key")
	}
	sums, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS"))
	check(err)
	sig, err := os.ReadFile(filepath.Join(dir, "SHA256SUMS.sig"))
	check(err)
	_, err = selfupdate.Verify(sums, sig, []ed25519.PublicKey{public})
	check(err)
	fmt.Println("verified")
}

// publish creates the GitHub release for tag at commit and uploads every
// file of the release directory to it.
func publish(dir, repo, tag, commit string) {
	token := os.Getenv("GITHUB_TOKEN")
	if token == "" || tag == "" || commit == "" {
		fail("publish needs GITHUB_TOKEN, -tag and -commit")
	}
	body, _ := json.Marshal(map[string]any{
		"tag_name": tag, "target_commitish": commit, "name": tag,
		"body": "Signed release of the PatchPilot agent. Verify SHA256SUMS.sig against the public key in internal/selfupdate.",
	})
	var release struct {
		UploadURL string `json:"upload_url"`
	}
	call(token, http.MethodPost, "https://api.github.com/repos/"+repo+"/releases", "application/json", body, &release)
	upload := strings.Split(release.UploadURL, "{")[0]

	entries, err := os.ReadDir(dir)
	check(err)
	names := []string{}
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		content, err := os.ReadFile(filepath.Join(dir, name))
		check(err)
		call(token, http.MethodPost, upload+"?name="+url.QueryEscape(name), "application/octet-stream", content, nil)
		fmt.Println("uploaded", name)
	}
}

func call(token, method, target, contentType string, body []byte, into any) {
	req, err := http.NewRequest(method, target, bytes.NewReader(body))
	check(err)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", contentType)
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	check(err)
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		fail(fmt.Sprintf("%s %s: %s %s", method, target, resp.Status, b))
	}
	if into != nil {
		check(json.Unmarshal(b, into))
	}
}

func check(err error) {
	if err != nil {
		fail(err.Error())
	}
}

func fail(message string) {
	fmt.Fprintln(os.Stderr, message)
	os.Exit(1)
}
