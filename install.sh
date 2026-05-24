#!/bin/sh
# cloudy installer
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/rlaope/cloudy/master/install.sh | sh
#
# Or, with custom install dir:
#   curl -fsSL https://raw.githubusercontent.com/rlaope/cloudy/master/install.sh | CLOUDY_INSTALL_DIR=/usr/local/bin sh
#
# Re-run the same one-liner anytime to upgrade — the script always pulls
# whatever GitHub marks as the "latest" release.

set -eu

REPO="rlaope/cloudy"
BIN_NAME="cloudy"
INSTALL_DIR="${CLOUDY_INSTALL_DIR:-$HOME/.local/bin}"

color_red()    { printf '\033[31m%s\033[0m\n' "$1"; }
color_green()  { printf '\033[32m%s\033[0m\n' "$1"; }
color_yellow() { printf '\033[33m%s\033[0m\n' "$1"; }
color_cyan()   { printf '\033[36m%s\033[0m\n' "$1"; }

# ─── Pre-flight checks ────────────────────────────────────────────────

for cmd in curl uname mkdir chmod; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        color_red "✗ required command not found: $cmd"
        exit 1
    fi
done

# ─── Detect OS/arch ───────────────────────────────────────────────────

OS_RAW="$(uname -s)"
case "$OS_RAW" in
    Linux)  OS="linux"  ;;
    Darwin) OS="darwin" ;;
    *)
        color_red "✗ unsupported operating system: $OS_RAW"
        echo "  cloudy releases binaries for linux and darwin only."
        echo "  For other platforms, build from source:"
        echo "    git clone https://github.com/$REPO.git && cd cloudy && make build"
        exit 1
        ;;
esac

ARCH_RAW="$(uname -m)"
case "$ARCH_RAW" in
    x86_64|amd64)  ARCH="amd64" ;;
    arm64|aarch64) ARCH="arm64" ;;
    *)
        color_red "✗ unsupported architecture: $ARCH_RAW"
        echo "  cloudy releases binaries for amd64 and arm64 only."
        exit 1
        ;;
esac

# ─── Resolve latest release tag ───────────────────────────────────────

color_cyan "→ querying GitHub for latest cloudy release…"
LATEST="$(curl -fsSL "https://api.github.com/repos/$REPO/releases/latest" \
    | grep '"tag_name"' \
    | head -n1 \
    | sed -E 's/.*"tag_name": *"([^"]+)".*/\1/')"

if [ -z "$LATEST" ]; then
    color_red "✗ could not determine the latest release tag"
    echo "  Check https://github.com/$REPO/releases manually."
    exit 1
fi

echo "  latest: $LATEST"
echo "  target: $OS/$ARCH"

# ─── Download the binary ──────────────────────────────────────────────

ASSET="cloudy-$OS-$ARCH"
URL="https://github.com/$REPO/releases/download/$LATEST/$ASSET"

color_cyan "→ downloading $URL"
TMP="$(mktemp -t cloudy.XXXXXX)"
trap 'rm -f "$TMP"' EXIT

if ! curl -fsSL "$URL" -o "$TMP"; then
    color_red "✗ download failed"
    echo "  Verify the asset exists: $URL"
    exit 1
fi

# Refuse to install something obviously not a binary — GitHub returns
# an HTML error page when the asset is missing, and we do not want
# that page chmod'd as an executable.
if head -c 32 "$TMP" | grep -q "<!DOCTYPE\|<html"; then
    color_red "✗ download looks like an HTML page, not a binary"
    echo "  The release probably does not have a $ASSET asset yet."
    exit 1
fi

# ─── Verify SHA-256 ──────────────────────────────────────────────────
#
# The release workflow publishes a per-asset .sha256 file. Verifying it
# closes the "asset got corrupted in flight" gap (and gives operators
# an end-to-end witness they can audit against the published value).
# Same TLS path as the binary itself, so this is not supply-chain
# attestation — it is integrity. Mirrors the in-process selfupdate
# path's behaviour (internal/selfupdate/selfupdate.go fetchSHA256).
#
# Pick the right hasher: macOS ships `shasum`, most linuxes ship
# `sha256sum`. Both produce the same shape "<hex>  <filename>".
if command -v sha256sum >/dev/null 2>&1; then
    HASHER="sha256sum"
elif command -v shasum >/dev/null 2>&1; then
    HASHER="shasum -a 256"
else
    color_yellow "⚠ no sha256sum/shasum on PATH — skipping integrity check"
    HASHER=""
fi

if [ -n "$HASHER" ]; then
    color_cyan "→ verifying SHA-256"
    SHA_URL="$URL.sha256"
    EXPECTED="$(curl -fsSL "$SHA_URL" 2>/dev/null | awk '{print $1}' | head -n1)"
    if [ -z "$EXPECTED" ] || [ "${#EXPECTED}" -ne 64 ]; then
        color_red "✗ could not fetch a valid .sha256 alongside the asset ($SHA_URL)"
        exit 1
    fi
    ACTUAL="$($HASHER "$TMP" | awk '{print $1}')"
    if [ "$ACTUAL" != "$EXPECTED" ]; then
        color_red "✗ SHA-256 mismatch"
        echo "  expected: $EXPECTED"
        echo "  actual:   $ACTUAL"
        echo "  Refusing to install. Re-download or report to https://github.com/$REPO/issues"
        exit 1
    fi
fi

# ─── Install ──────────────────────────────────────────────────────────

mkdir -p "$INSTALL_DIR"
INSTALLED="$INSTALL_DIR/$BIN_NAME"
mv "$TMP" "$INSTALLED"
chmod +x "$INSTALLED"
trap - EXIT

color_green "✓ installed: $INSTALLED"

# ─── PATH check ───────────────────────────────────────────────────────

case ":$PATH:" in
    *":$INSTALL_DIR:"*)
        # already on PATH — `cloudy` will resolve.
        ;;
    *)
        color_yellow ""
        color_yellow "⚠ $INSTALL_DIR is not on your PATH."
        echo "  Add this line to your shell's rc file (~/.zshrc, ~/.bashrc):"
        echo
        printf '    \033[36mexport PATH="%s:$PATH"\033[0m\n' "$INSTALL_DIR"
        echo
        echo "  Or run cloudy with the full path: $INSTALLED"
        ;;
esac

# ─── Verify ────────────────────────────────────────────────────────────

if VERSION="$("$INSTALLED" --version 2>&1)"; then
    color_green "✓ $VERSION"
else
    color_yellow "⚠ installed binary did not respond to --version cleanly"
fi

echo
color_cyan "next:  cloudy   (or '$INSTALLED' if not on PATH)"
color_cyan "upgrade: re-run this installer — it always pulls the latest tag."
