#!/usr/bin/env sh
set -eu

OWNER_REPO="${PB_REPO:-yesabhishek/pastebin-cli}"
BIN_DIR="${PB_BIN_DIR:-$HOME/.local/bin}"
NAME="pb"

OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64) ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
esac

API_URL="https://api.github.com/repos/$OWNER_REPO/releases/latest"
TAG="$(curl -fsSL "$API_URL" | sed -n 's/.*"tag_name":[[:space:]]*"\([^"]*\)".*/\1/p' | head -n1)"
if [ -z "$TAG" ]; then
  echo "failed to resolve latest release tag from $API_URL" >&2
  exit 1
fi

ASSET="${NAME}_${OS}_${ARCH}.tar.gz"
URL="https://github.com/$OWNER_REPO/releases/download/$TAG/$ASSET"

mkdir -p "$BIN_DIR"
TMP_DIR="$(mktemp -d)"
trap 'rm -rf "$TMP_DIR"' EXIT INT TERM

curl -fsSL "$URL" -o "$TMP_DIR/$ASSET"
tar -xzf "$TMP_DIR/$ASSET" -C "$TMP_DIR"
install "$TMP_DIR/$NAME" "$BIN_DIR/$NAME"

printf 'Installed %s to %s\n' "$NAME" "$BIN_DIR/$NAME"
printf 'Add %s to your PATH if needed.\n' "$BIN_DIR"
