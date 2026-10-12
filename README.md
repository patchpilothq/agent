# PatchPilot agent

The open-source agent of [PatchPilot](https://patchpilot.cc), which shows
the patch status of every Linux server you run in one dashboard.

The agent runs on each server. It reports which packages are installed and
which updates are waiting, and it installs those updates when you press
"update now" or inside the maintenance window you set.

**Status:** in development. There is no release yet.

## What it does, and what it does not

- **It runs as root**, because installing packages needs root.
- **It only connects outbound.** It opens the connection to PatchPilot; no
  port is opened on your server, and PatchPilot holds no SSH key.
- **It can do two things:** report the server's package state, and install
  pending updates of packages that are already installed, from the package
  sources your server already uses. It has no remote shell and runs no
  command it is sent. It never adds a package source, never removes a
  package and never reboots.
- **It sends your server's package and system state** — never your files,
  your databases or your environment.
- **Removing it changes nothing.** Your package sources and configuration
  stay as they were.

This repository is where you can check every line of that.

## Building

Go 1.26:

```
make precommit   # gofmt, go vet, tests
make build       # static binaries for amd64, arm64 and armhf in dist/
```

## Contributing

Issues and pull requests are welcome. This repository is a mirror of the
directory the agent is developed in, so an accepted pull request is applied
there and arrives here with its author kept.

## Licence

GNU General Public License v3.0 or later — see [`LICENSE`](LICENSE).
