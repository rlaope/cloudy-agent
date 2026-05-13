#!/usr/bin/env bash
# Bastion install helper for cloudy. Idempotent.
#
# - installs the cloudy binary into /usr/local/bin
# - drops a /etc/profile.d/cloudy.sh that sets CLOUDY_HOME per user
# - copies the systemd template unit if systemd is present
#
# Usage:
#   sudo ./install.sh [--binary /path/to/cloudy] [--state-root /var/cloudy]

set -euo pipefail

BINARY="${BINARY:-./cloudy}"
STATE_ROOT="${STATE_ROOT:-/var/cloudy}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --binary)     BINARY="$2"; shift 2 ;;
    --state-root) STATE_ROOT="$2"; shift 2 ;;
    -h|--help)
      sed -n '2,12p' "$0"; exit 0 ;;
    *) echo "unknown flag: $1" >&2; exit 2 ;;
  esac
done

if [[ "$EUID" -ne 0 ]]; then
  echo "install.sh must be run as root" >&2
  exit 1
fi

if [[ ! -x "$BINARY" ]]; then
  echo "binary not found or not executable: $BINARY" >&2
  exit 1
fi

install -m 0755 "$BINARY" /usr/local/bin/cloudy
echo "installed: /usr/local/bin/cloudy"

mkdir -p "$STATE_ROOT"
chmod 0755 "$STATE_ROOT"

cat > /etc/profile.d/cloudy.sh <<EOF
# cloudy bastion defaults; managed by manifests/bastion/install.sh
export CLOUDY_HOME="${STATE_ROOT}/\$USER"
mkdir -p "\$CLOUDY_HOME" 2>/dev/null || true
EOF
chmod 0644 /etc/profile.d/cloudy.sh
echo "wrote:     /etc/profile.d/cloudy.sh (CLOUDY_HOME=${STATE_ROOT}/\$USER)"

if command -v systemctl >/dev/null 2>&1; then
  install -m 0644 "$(dirname "$0")/cloudy@.service" /etc/systemd/system/cloudy@.service
  systemctl daemon-reload
  echo "installed: /etc/systemd/system/cloudy@.service (enable per-user with: systemctl enable --now cloudy@<user>)"
fi

echo
echo "Done. Each shell user must:"
echo "  1. log out and back in (so /etc/profile.d/cloudy.sh sources)"
echo "  2. run 'cloudy setup' once to populate \$CLOUDY_HOME/config.yaml"
echo "  3. run 'cloudy doctor' to verify"
