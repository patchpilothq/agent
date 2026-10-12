# The agent's checks and builds. `make precommit` is the full local check and
# runs before any change to the agent counts as done.

# Module mode regardless of the caller's environment.
export GO111MODULE := on

VERSION ?= dev
# DEV_PUBLIC_KEY adds a development release key to a development build;
# release builds never set it (internal/selfupdate).
DEV_PUBLIC_KEY ?=
GOFLAGS_BUILD := -trimpath -buildvcs=false -ldflags "-s -w -X main.version=$(VERSION) -X github.com/patchpilothq/agent/internal/selfupdate.devKey=$(DEV_PUBLIC_KEY)"

.PHONY: precommit inventory build release clean

precommit:
	@unformatted="$$(gofmt -l .)"; if [ -n "$$unformatted" ]; then echo "not gofmt-formatted:"; echo "$$unformatted"; exit 1; fi
	go vet ./...
	go test ./...

# Regenerates DEPENDENCIES.md after a dependency changed.
inventory:
	go test -run TestInventoryMatchesTheModuleGraph -update-inventory .

# A release in dist/: static binaries for every architecture the agent
# supports — armhf for ARMv6, so it also runs on the oldest 32-bit Raspberry
# Pis — the install scripts, and SHA256SUMS over all of them.
build:
	rm -rf dist
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build $(GOFLAGS_BUILD) -o dist/patchpilot-agent-linux-amd64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build $(GOFLAGS_BUILD) -o dist/patchpilot-agent-linux-arm64 .
	CGO_ENABLED=0 GOOS=linux GOARCH=arm GOARM=6 go build $(GOFLAGS_BUILD) -o dist/patchpilot-agent-linux-armhf .
	cp install/install.sh install/uninstall.sh dist/
	printf '%s\n' "$(VERSION)" >dist/VERSION
	cd dist && $$(command -v sha256sum || echo shasum -a 256) patchpilot-agent-linux-* install.sh uninstall.sh VERSION >SHA256SUMS

# A release: the build, signed. PATCHPILOT_SIGNING_KEY in the environment.
release: build
	go run ./tools/release sign -dir dist

clean:
	rm -rf dist
