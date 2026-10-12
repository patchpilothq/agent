# Agent working rules (`agent/`)

These rules cover the PatchPilot agent: a Go program that runs as root on a
customer's Debian or Ubuntu server. Repository-wide rules — how to work,
plan and ADR duties, commit and staging discipline — live in the root
`AGENTS.md` of the PatchPilot repository; the architectural rules they
specialise live in its `docs/ARCHITECTURE_PRINCIPLES.md`. Its
`docs/ARCHITECTURE.md` §"Agent" describes this unit — read it before making
changes here.

This directory is published, with its history, to the public repository
`patchpilothq/agent`. Everything written here is read by the public,
including this file.

## Toolchain

- **(MUST)** Run every command from `agent/`, through `make`, which forces
  module mode whatever `GO111MODULE` the shell has. Go 1.26.
- **(MUST)** Keep the Go version identical in `go.mod` and
  `.woodpecker/agent.yaml`.
- **(MUST)** Build with `make build`: static binaries (`CGO_ENABLED=0`) for
  amd64, arm64 and armhf (ARMv6). A binary that links the system's C
  library stops running on the old releases the product exists to show.

## Verifying a change

- **(MUST)** Run `make precommit` before the work counts as done: `gofmt`,
  `go vet` and the tests.
- **(MUST)** A change to what the agent does on a server is verified on a
  real machine of the release it concerns, not in a container without
  systemd — the test matrix is in the PatchPilot repository's
  `plans/FIRST_RELEASE.md`.

## Tests

- **(SHOULD)** Test through `run` and the agent's own functions with
  in-memory writers and temporary directories; nothing in the unit tests
  touches the machine's package manager.

## Dependencies

- **(MUST)** Add a dependency only when the standard library cannot do the
  job. Every dependency runs as root on every customer server, and every
  customer who audits the agent has to read it.
- **(MUST)** Regenerate `DEPENDENCIES.md` with `make inventory` whenever
  `go.mod` changes. `inventory_test.go` fails while it is stale, and when a
  dependency's licence is not compatible with the agent's GPL v3.

## What the agent may do

- **(MUST NOT)** Give the agent a capability beyond reporting the server's
  inventory and installing pending updates of installed packages from the
  sources configured on the machine. No remote shell, no command line
  received from the server, no new package source, no removed package, no
  reboot. The closed list is what limits a compromise of PatchPilot, and the
  website promises it; widening it is a new architecture decision, made
  before the code.
- **(MUST NOT)** Open a listening port, use or store an SSH key, change the
  machine's configuration, or send anything beyond the server's package and
  system state. Each of these is published as something the agent does not
  do.

## Coding rules

- `gofmt` decides the layout. Errors are returned and wrapped with context,
  never ignored.
