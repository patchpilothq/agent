#!/bin/sh
# Installs the PatchPilot agent on a Debian or Ubuntu server.
#
#   curl -fsSL https://patchpilot.cc/install.sh | sudo sh -s -- <enrollment token>
#
# Read it before you run it; it is short on purpose. It
#
#   - creates the system user `patchpilot`, which the agent runs as — the
#     agent never runs as root;
#   - downloads the agent for this machine's architecture from PatchPilot
#     and checks it against the release's SHA256SUMS;
#   - writes the update step: the one script, and the two systemd units that
#     start it, that run as root, and only `apt-get` with the options fixed
#     below. The agent can ask it to refresh the package lists or to install
#     pending updates, and nothing else;
#   - enrolls the machine with your workspace's token and starts the agent.
#
# It adds no package source and changes no configuration of the machine.
# uninstall.sh removes everything it creates. PROTOCOL.md describes what the
# agent sends and how it talks to the update step.
set -eu

server=https://patchpilot.cc
token=
while [ $# -gt 0 ]; do
	case $1 in
	--server) server=${2:?--server needs a URL}; shift 2 ;;
	-*) echo "unknown option: $1" >&2; exit 2 ;;
	*) token=$1; shift ;;
	esac
done
server=${server%/}

fail() { echo "PatchPilot: $*" >&2; exit 1; }

[ -n "$token" ] || fail "the enrollment token is missing — copy the whole command from the dashboard."
case $server in https://* | http://*) ;; *) fail "not a server address: $server" ;; esac
[ "$(id -u)" = 0 ] || fail "run this as root, for example with sudo."
[ -d /run/systemd/system ] || fail "this machine does not run systemd."
command -v apt-get >/dev/null && command -v dpkg >/dev/null || fail "this machine has no apt; PatchPilot supports Debian, Ubuntu and their derivatives."

arch=$(dpkg --print-architecture)
case $arch in amd64 | arm64 | armhf) ;; *) fail "the architecture $arch is not supported (amd64, arm64 and armhf are)." ;; esac

if command -v curl >/dev/null; then
	fetch() { curl -fsSL --proto '=https,http' -o "$2" "$1"; }
elif command -v wget >/dev/null; then
	fetch() { wget -q -O "$2" "$1"; }
else
	fail "neither curl nor wget is installed."
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

echo "Downloading the PatchPilot agent for $arch …"
binary=patchpilot-agent-linux-$arch
fetch "$server/agent/download/$binary" "$tmp/$binary" || fail "could not download the agent from $server."
fetch "$server/agent/download/SHA256SUMS" "$tmp/SHA256SUMS" || fail "could not download the agent's checksums from $server."
(cd "$tmp" && grep " $binary\$" SHA256SUMS | sha256sum -c --status) || fail "the downloaded agent does not match its checksum; nothing was installed."

echo "Installing …"
if ! id patchpilot >/dev/null 2>&1; then
	useradd --system --user-group --home-dir /var/lib/patchpilot --no-create-home \
		--shell /usr/sbin/nologin --comment "PatchPilot agent" patchpilot
fi

# The agent's own files: its binary, which it replaces when it updates
# itself, and its credential.
install -d -o patchpilot -g patchpilot -m 0755 /opt/patchpilot
install -o patchpilot -g patchpilot -m 0755 "$tmp/$binary" /opt/patchpilot/patchpilot-agent
install -d -o patchpilot -g patchpilot -m 0700 /var/lib/patchpilot

# Its configuration, which only root can change.
install -d -o root -g root -m 0755 /etc/patchpilot
printf 'server=%s\n' "$server" >"$tmp/agent.conf"
install -o root -g patchpilot -m 0640 "$tmp/agent.conf" /etc/patchpilot/agent.conf

# The update step's directories: the agent may create files in requests/,
# and may read what the step writes to runs/, which only root can write.
install -d -o root -g root -m 0755 /var/lib/patchpilot-update
install -d -o patchpilot -g patchpilot -m 0755 /var/lib/patchpilot-update/requests
install -d -o root -g patchpilot -m 0750 /var/lib/patchpilot-update/runs

# The update step. It runs as root, so it reads nothing the agent wrote but
# which of four names exist in requests/, and runs nothing but apt-get with
# the options below.
install -d -o root -g root -m 0755 /usr/local/lib/patchpilot
cat >"$tmp/update-step" <<'STEP'
#!/bin/sh
# PatchPilot's update step: the only part of PatchPilot that runs as root.
# Written by PatchPilot's install.sh; started by patchpilot-update.path when
# the agent creates one of these empty files in
# /var/lib/patchpilot-update/requests/:
#
#   refresh   apt-get update
#   security  install the pending security updates
#   upgrade   install the pending security updates, then the rest
#
# and while it installs, a file named stop ends the run after the package
# in progress. Nothing else the agent wrote is read. Every run is
# non-interactive, keeps the machine's configuration files, respects holds
# and phased updates, never installs a new package and never removes one;
# what an upgrade would not install, it leaves alone.
set -u
requests=/var/lib/patchpilot-update/requests
runs=/var/lib/patchpilot-update/runs

mode=
for m in upgrade security refresh; do
	if [ -e "$requests/$m" ]; then mode=$m; break; fi
done
rm -f "$requests/upgrade" "$requests/security" "$requests/refresh" "$requests/stop"
[ -n "$mode" ] || exit 0

# Only the output file gets its own mode; apt runs with the machine's umask,
# so that a log or directory it creates is as it would be without us.
: >"$runs/output"
chgrp patchpilot "$runs/output"
chmod 0640 "$runs/output"
exec >>"$runs/output" 2>&1

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
export DEBIAN_FRONTEND=noninteractive NEEDRESTART_MODE=l NEEDRESTART_SUSPEND=1 LC_ALL=C LANG=C

# apt-get, waiting up to an hour while another apt or unattended-upgrades
# holds dpkg's lock.
apt_get() {
	waited=0
	while :; do
		{ apt-get -q -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold "$@" 2>&1; echo $? >"$tmp/status"; } | tee "$tmp/out"
		status=$(cat "$tmp/status")
		if [ "$status" != 0 ] && grep -q -e 'Could not get lock' -e 'Unable to acquire the dpkg' "$tmp/out" && [ "$waited" -lt 3600 ]; then
			echo "PATCHPILOT wait dpkg-lock"
			sleep 15
			waited=$((waited + 15))
			continue
		fi
		return "$status"
	done
}

echo "PATCHPILOT start $mode $(date -u +%Y-%m-%dT%H:%M:%SZ)"
apt_get update
result=$?
if [ "$mode" = refresh ]; then
	echo "PATCHPILOT done $result"
	exit 0
fi

# What a plain upgrade would install, as apt itself decides it: security
# updates first.
apt-get -s -o Debug::NoLocking=true upgrade >"$tmp/plan" 2>/dev/null
grep '^Inst ' "$tmp/plan" | grep -i 'security' | cut -d' ' -f2 >"$tmp/packages"
if [ "$mode" = upgrade ]; then
	grep '^Inst ' "$tmp/plan" | grep -iv 'security' | cut -d' ' -f2 >>"$tmp/packages"
fi

# Installing a package by name marks it as installed by hand; the marks are
# put back afterwards, so that the machine's autoremove keeps working as it
# did.
apt-mark showauto >"$tmp/auto" 2>/dev/null

total=$(wc -l <"$tmp/packages" | tr -d ' ')
i=0
result=0
for package in $(cat "$tmp/packages"); do
	if [ -e "$requests/stop" ]; then
		echo "PATCHPILOT stopped"
		break
	fi
	i=$((i + 1))
	echo "PATCHPILOT package $i $total $package"
	apt_get install --only-upgrade --no-remove "$package"
	status=$?
	if [ "$status" != 0 ]; then
		echo "PATCHPILOT failed $package $status"
		result=1
	fi
done

if [ -s "$tmp/auto" ]; then
	xargs apt-mark auto <"$tmp/auto" >/dev/null 2>&1
fi
rm -f "$requests/stop"
echo "PATCHPILOT done $result"
STEP
install -o root -g root -m 0755 "$tmp/update-step" /usr/local/lib/patchpilot/update-step

cat >/etc/systemd/system/patchpilot-update.path <<'UNIT'
# Written by PatchPilot's install.sh: starts the update step when the agent
# asks for one of its three modes.
[Unit]
Description=PatchPilot update step, waiting for a request

[Path]
PathExists=/var/lib/patchpilot-update/requests/refresh
PathExists=/var/lib/patchpilot-update/requests/security
PathExists=/var/lib/patchpilot-update/requests/upgrade
Unit=patchpilot-update.service

[Install]
WantedBy=multi-user.target
UNIT

cat >/etc/systemd/system/patchpilot-update.service <<'UNIT'
# Written by PatchPilot's install.sh: the update step, as root.
[Unit]
Description=PatchPilot update step

[Service]
Type=oneshot
ExecStart=/usr/local/lib/patchpilot/update-step
TimeoutStartSec=infinity
UNIT

cat >/etc/systemd/system/patchpilot-agent.service <<'UNIT'
# Written by PatchPilot's install.sh: the agent, as its own user, sandboxed.
[Unit]
Description=PatchPilot agent
Documentation=https://github.com/patchpilothq/agent
Wants=network-online.target
After=network-online.target

[Service]
User=patchpilot
Group=patchpilot
ExecStart=/opt/patchpilot/patchpilot-agent run
Restart=always
RestartSec=10
# 78: the server was removed from PatchPilot; restarting would not help.
RestartPreventExitStatus=78
UMask=0077

NoNewPrivileges=yes
CapabilityBoundingSet=
AmbientCapabilities=
ProtectSystem=strict
ReadWritePaths=/opt/patchpilot /var/lib/patchpilot /var/lib/patchpilot-update/requests
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
SystemCallArchitectures=native

[Install]
WantedBy=multi-user.target
UNIT

echo "Connecting this server to PatchPilot …"
printf '%s\n' "$token" | runuser -u patchpilot -- /opt/patchpilot/patchpilot-agent enroll ||
	fail "this server could not be connected. Copy the command again from the dashboard; the token may have been replaced."

systemctl daemon-reload
systemctl enable --quiet patchpilot-update.path patchpilot-agent.service
systemctl restart patchpilot-update.path patchpilot-agent.service

echo "Done. $(hostname) appears in your PatchPilot dashboard within a minute."
