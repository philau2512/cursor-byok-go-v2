#!/usr/bin/env bash
# Build the macOS app bundle (DMG) on a native macOS host, e.g. Mac mini M1.
# This is the macOS counterpart of build.bat on Windows: it resolves the app
# version from build/config.yml and emits bin/cursor-byok-<version>.dmg,
# appending (1), (2), ... when the file already exists.
#
# Usage:
#   bash build-mac.sh              # build for the current Mac architecture
#   bash build-mac.sh amd64        # force Intel (amd64) build
#   bash build-mac.sh arm64        # force Apple Silicon (arm64) build
set -euo pipefail
cd "$(dirname "$0")"

for cmd in go task; do
  if ! command -v "$cmd" >/dev/null 2>&1; then
    echo "ERROR: '$cmd' not found in PATH. Install it first (e.g. brew install go go-task)." >&2
    exit 1
  fi
done

# Map host machine to Go architecture; allow an explicit override argument.
ARCH="${1:-}"
if [ -z "$ARCH" ]; then
  case "$(uname -m)" in
    arm64|aarch64) ARCH=arm64 ;;
    x86_64) ARCH=amd64 ;;
    *) ARCH="$(uname -m)" ;;
  esac
fi
if [ "$ARCH" != "arm64" ] && [ "$ARCH" != "amd64" ]; then
  echo "ERROR: unsupported ARCH '$ARCH' (expected arm64 or amd64)." >&2
  exit 1
fi

VERSION="$(go run ./scripts/release version -config ./build/config.yml | tr -d '[:space:]')"
if [ -z "$VERSION" ]; then
  echo "ERROR: failed to resolve app version from build/config.yml." >&2
  exit 1
fi

mkdir -p bin
BASE_NAME="cursor-byok-${VERSION}"
DMG_FILE="${BASE_NAME}.dmg"
INDEX=1
while [ -e "bin/${DMG_FILE}" ]; do
  DMG_FILE="${BASE_NAME}(${INDEX}).dmg"
  INDEX=$((INDEX + 1))
done

task build:darwin:dmg ARCH="$ARCH" DMG_NAME="$DMG_FILE"

echo "Created: bin/${DMG_FILE}"
