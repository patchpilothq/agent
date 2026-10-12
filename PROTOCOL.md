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

`report` — after `hello`, and again whenever any of it changes. It is the
whole state, never a difference.

```json
{"type": "report",
 "system": {
   "hostname": "web-1",
   "os_id": "debian", "os_version_id": "12", "os_codename": "bookworm",
   "os_name": "Debian GNU/Linux 12 (bookworm)",
   "arch": "amd64", "kernel": "6.1.0-25-amd64",
   "machine_id_hash": "…"},
 "packages": [["openssl", "3.0.14-1~deb12u1", "amd64"], …],
 "updates": [
   {"name": "openssl", "from": "3.0.14-1~deb12u1", "to": "3.0.15-1~deb12u1",
    "origins": ["Debian-Security:12/stable-security"],
    "security": true, "held_back": false}],
 "held": ["linux-image-amd64"],
 "reboot": {"required": true, "packages": ["linux-image-6.1.0-26-amd64"]},
 "last_upgrade_at": "2026-10-11T03:12:40Z",
 "pro": {"attached": false}}
```

- `packages` — every installed package: name, version, architecture
  (dpkg's status file).
- `updates` — what an upgrade would install, from `apt-get -s dist-upgrade`
  run without root: `origins` are apt's, `security` is true when one of them
  is a security archive, and `held_back` when a plain upgrade would not
  install it (it needs a removal or a new package, or it is phased).
- `held` — packages put on hold (`apt-mark showhold`).
- `reboot` — `/var/run/reboot-required` and the packages it names.
- `last_upgrade_at` — the last `Upgrade:` in apt's history log, if any.
- `pro` — whether Ubuntu Pro is attached, on Ubuntu only.

That list is everything the agent reads. It reads no file contents beyond
these, no databases, no environment.

### From the server

`welcome` — after `hello`: `{"type": "welcome", "server_id": "<uuid>"}`.

Later protocol versions add the run and the agent's own update; until then
the server sends nothing else.

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
only root can write and the agent can read. Its own lines start with
`PATCHPILOT `:

```
PATCHPILOT start refresh 2026-10-12T03:00:00Z
PATCHPILOT wait dpkg-lock
PATCHPILOT package 3 12 openssl
PATCHPILOT failed openssl 100
PATCHPILOT done 0
```
