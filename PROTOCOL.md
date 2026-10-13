# The agent's protocol

How the PatchPilot agent talks to the PatchPilot server, and how it talks to
the update step on its own machine. Both are public on purpose: what the
agent sends and what it can be asked to do is the whole of the relationship
between a server and PatchPilot, and anyone can check this file against the
code.

The format is versioned. A field is added without a new version; a field is
never removed or changed in meaning while an agent that relies on it may
still be running. A message with an unknown `type`, and an unknown field,
is ignored by both sides.

## Enrolling

Once, at install, the agent reads the workspace's enrollment token from
standard input and sends:

```
POST /api/agents/enroll
Content-Type: application/json

{"token": "<enrollment token>",
 "machine_id_hash": "<hex HMAC-SHA256 of /etc/machine-id>",
 "hostname": "web-1"}
```

The machine ID is never sent: only an HMAC of it under the fixed key
`patchpilot-agent-machine-id`, which recognises the same machine again and
nothing else.

The answer is `201 {"server_id": "<uuid>", "credential": "<secret>"}`, or
`401` for an unknown token, `429` when rate-limited, `422` for a malformed
request. The agent keeps both values in `/var/lib/patchpilot/credential`,
readable by its own user only.

## The socket

The agent then opens one WebSocket and keeps it open, reconnecting with
growing waits between 1 and 60 seconds:

```
GET /api/agents/socket
Authorization: Bearer <server_id>.<credential>
```

`401` means the server was removed or reinstalled elsewhere; the agent then
exits with status 78, and systemd does not restart it. Every message is one
JSON object in a text frame, with a `type`.

The server pings every 30 seconds; a connection that has heard nothing for
90 seconds is closed by whichever side notices, and the agent reconnects.

### From the agent

`hello` — first, on every connection.

```json
{"type": "hello", "protocol": 1, "agent_version": "0.1.0"}
```

`report` — the whole state: after `welcome` when the server holds no state
of this agent it can build on, and whenever the server asks with
`report_needed`. Every other change goes as a `report_delta` (below).

```json
{"type": "report",
 "system": {
   "hostname": "web-1",
   "os_id": "debian", "os_version_id": "12", "os_codename": "bookworm",
   "os_name": "Debian GNU/Linux 12 (bookworm)", "os_variant": "raspios",
   "arch": "amd64", "kernel": "6.1.0-25-amd64",
   "machine_id_hash": "…"},
 "packages": [["openssl", "3.0.14-1~deb12u1", "amd64"], …],
 "updates": [
   {"name": "libssl3", "source": "openssl",
    "from": "3.0.14-1~deb12u1", "to": "3.0.15-1~deb12u1",
    "origins": ["Debian-Security:12/stable-security"],
    "security": true, "held_back": false}],
 "held": ["linux-image-amd64"],
 "reboot": {"required": true, "packages": ["linux-image-6.1.0-26-amd64"]},
 "last_upgrade_at": "2026-10-11T03:12:40Z",
 "pro": {"attached": false}}
```

- `system` — from `/proc/sys/kernel`, `/etc/os-release`, `/etc/machine-id`
  (hashed) and `dpkg --print-architecture`; `os_variant` is `raspios` when
  `/etc/rpi-issue` exists, because Raspberry Pi OS's 64-bit edition calls
  itself Debian, and is left out otherwise.
- `packages` — every installed package: name, version, architecture
  (dpkg's status file).
- `updates` — what an upgrade would install, from `apt-get -s dist-upgrade`
  run without root: `source` is the source package it was built from
  (dpkg's status file), under which security data is published; `origins`
  are apt's, `security` is true when one of them is a security archive, and
  `held_back` when a plain upgrade would not install it (it needs a removal
  or a new package, or it is phased, or it is on hold).
- `held` — packages put on hold (`apt-mark showhold`).
- `reboot` — `/var/run/reboot-required` and the packages it names.
- `last_upgrade_at` — the last `upgrade` in dpkg's log (`/var/log/dpkg.log`
  and its rotations), if any; apt's history log is not readable by the
  agent's user everywhere.
- `pro` — whether Ubuntu Pro is attached, on Ubuntu only.

`lists` — the outcome of the update step's latest refresh of the package
lists, after `welcome` and whenever it changes:
`{"type": "lists", "refreshed_at": "2026-10-12T03:00:41Z", "error": null}`.

- `refreshed_at` — when the update step last refreshed the package lists
  without an error (`null` until it has); `updates` is only as recent as
  this.
- `error` — what apt said when the update step's latest refresh failed: its
  `E:` and `W:` lines, at most ten; `null` when it succeeded.

Agents up to 0.1.0 sent both inside `report`, as `lists_refreshed_at` and
`lists_error`; the server still reads them there.

That is everything the agent reads. It reads no file contents beyond these,
no databases, no environment.

### Only what changed

After the first `report`, an agent sends what changed since a state the
server acknowledged, never the whole state again (owner, 2026-10-13).

**A state's digest.** Agent and server name a state by the SHA-256 of its
canonical text, in lowercase hex. The canonical text has one line per item,
each a tab-separated row ending in a line feed, and the lines are sorted
by their bytes:

```
system	<hostname>	<os_id>	<os_version_id>	<os_codename>	<os_name>	<os_variant>	<arch>	<kernel>
package	<name>	<version>	<architecture>
update	<name>	<source>	<from>	<to>	<origins, sorted, joined by ",">	<security: 1 or 0>	<held_back: 1 or 0>
held	<name>
reboot	<required: 1 or 0>	<packages, sorted, joined by ",">
last_upgrade_at	<RFC 3339 in UTC to the second, or nothing>
pro	<attached: 1 or 0, or nothing off Ubuntu>
```

A missing value is empty; an update's missing `source` is its `name`. A tab,
a line feed or a carriage return inside a value counts as a space.
`machine_id_hash` and `lists` are no part of it. `testdata/digest/` holds a
report and its digest, which the agent's and the server's tests both check.

`report_delta` — the changes from the state named `base` to the state named
`digest`:

```json
{"type": "report_delta",
 "base": "<digest the server acknowledged>",
 "digest": "<digest of the state after it>",
 "system": {…}, "held": […], "reboot": {…},
 "last_upgrade_at": "…", "pro": {…},
 "packages": {"set": [["openssl", "3.0.15-1~deb12u1", "amd64"]],
              "removed": [["libssl1.1", "amd64"]]},
 "updates": {"set": [{"name": "libc6", …}],
             "removed": ["openssl"]}}
```

`system`, `held`, `reboot`, `last_upgrade_at` and `pro` are whole, as in
`report`. `packages.set` adds or replaces packages by name and
architecture, `packages.removed` names the ones gone; `updates.set` adds or
replaces updates by name, `updates.removed` names the ones gone.

The agent keeps the last state the server acknowledged and sends one delta
at a time, the next only after the server answered. It sends nothing when
its state is the one the server holds.

### From the server

`welcome` — after `hello`:
`{"type": "welcome", "server_id": "<uuid>", "report_digest": "<digest>"}`,
where `report_digest` is the digest of the state the server holds for this
agent, or `null`. A server whose `welcome` has no `report_digest` key at all
reads only `report`, and the agent sends it the whole state on every
change.

`report_ack` — `{"type": "report_ack", "digest": "<digest>"}`: the server
holds the state of the `report` or `report_delta` it just read.

`report_needed` — `{"type": "report_needed"}`: send the whole state. The
server sends it after a `report_delta` it could not apply — an unknown
base, a digest that does not match the state after applying, a message out
of bounds — and at any time when it holds no state it trusts. The agent
answers with a `report`.

`run` — install the pending updates now:
`{"type": "run", "run_id": "<uuid>", "mode": "upgrade"}`, where `mode` is
`upgrade` (security updates first, then the rest) or `security`. The agent
asks the update step for that mode and nothing else.

`stop` — `{"type": "stop", "run_id": "<uuid>"}`: finish the package in
progress and install no further one (a maintenance window closing).

`update_agent` — `{"type": "update_agent", "version": "0.2.0"}`: a newer
release exists. The agent downloads it from `<server>/agent/download/` —
`SHA256SUMS`, `SHA256SUMS.sig`, `VERSION` and its binary — and replaces its
own binary only when the signature over `SHA256SUMS` verifies against a
public key compiled into it (`internal/selfupdate`), the version is the one
announced and newer than its own, every file matches its sum, and the new
binary runs. Then it exits, and systemd starts the new one. Never during a
run, and never the update step: that changes only with a reinstall.

### Runs, from the agent

While a run goes on, the agent follows the update step's output and sends:

```json
{"type": "run_progress", "run_id": "…", "state": "installing",
 "done": 2, "total": 12, "package": "openssl"}
```

`state` is `refreshing` (the package lists), `waiting` (another apt, such as
`unattended-upgrades`, holds dpkg's lock) or `installing`. At the end:

```json
{"type": "run_finished", "run_id": "…", "result": "failed",
 "installed": ["openssl", "tzdata"],
 "failed": [{"package": "libc6", "output": "<apt's last lines for it>"}],
 "output": "<the step's last lines>"}
```

`result` is `succeeded`, `failed` or `stopped`. A run goes on when the
connection drops, and when the agent restarts; its result is kept in
`/var/lib/patchpilot/run.json` until it was sent, on the next connection if
need be. What an upgrade would not install — held packages, phased
updates, updates that need a removal — is left alone, and the report after
the run lists it as `held_back`.

## The update step

The only part of PatchPilot that runs as root after install — the agent
itself runs as its own unprivileged user — is the script
`/usr/local/lib/patchpilot/update-step`, started by the systemd units
`patchpilot-update.path` and `patchpilot-update.service`, all written by
`install.sh` in this directory.

The agent asks for a step by creating an empty file in
`/var/lib/patchpilot-update/requests/`, named after the mode:

- `refresh` — `apt-get update`;
- `security` — install the pending security updates;
- `upgrade` — install the pending security updates, then the rest.

The step reads nothing but which of those three names exist, removes them,
and writes its output to `/var/lib/patchpilot-update/runs/output`, which
only root can write and the agent can read. Every mode refreshes the lists
first, and records the outcome beside it: `lists-refreshed` holds the time
of the latest refresh without an error, `lists-error` apt's lines from the
latest failed one, removed again by the next success. apt-get exits 0 when
an index could not be fetched, so an `Err:` line in its output counts as a
failure. The agent asks for a refresh when it starts and whenever the
latest one is six hours old. Its own lines start with `PATCHPILOT `:

```
PATCHPILOT start refresh 2026-10-12T03:00:00Z
PATCHPILOT wait dpkg-lock
PATCHPILOT package 3 12 openssl
PATCHPILOT failed openssl 100
PATCHPILOT broken
PATCHPILOT done 1
```

After a failed package the step finishes configuring what dpkg unpacked
(`dpkg --configure -a`); when apt's dependencies are still broken, it stops
with `broken` instead of failing every further package, because mending them
can mean removing a package.
