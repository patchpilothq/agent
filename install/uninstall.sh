#!/bin/sh
# Removes the PatchPilot agent and everything install.sh created: the
# services, the update step, the agent's files and its user. The machine's
# package sources and configuration were never touched and stay as they are.
#
#   curl -fsSL https://patchpilot.cc/uninstall.sh | sudo sh
#
# The server stays in your PatchPilot workspace, shown as offline, until you
# remove it there.
set -eu

[ "$(id -u)" = 0 ] || { echo "PatchPilot: run this as root, for example with sudo." >&2; exit 1; }

for unit in patchpilot-agent.service patchpilot-update.path patchpilot-update.service; do
	systemctl disable --now --quiet "$unit" 2>/dev/null || true
	rm -f "/etc/systemd/system/$unit"
done
systemctl daemon-reload

rm -rf /opt/patchpilot /var/lib/patchpilot /var/lib/patchpilot-update \
	/etc/patchpilot /usr/local/lib/patchpilot

if id patchpilot >/dev/null 2>&1; then
	userdel patchpilot
fi
if getent group patchpilot >/dev/null 2>&1; then
	groupdel patchpilot
fi

echo "The PatchPilot agent is removed from $(hostname)."
