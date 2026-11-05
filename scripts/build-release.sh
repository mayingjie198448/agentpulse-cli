#!/usr/bin/env bash
set -euo pipefail

# scripts/build-release.sh
# Builds bare release binaries for supported architectures and generates SHA256SUMS.
# Replaces legacy .app bundle release artifacts per 0.5.0 specification.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
DIST_DIR="${ROOT_DIR}/dist"

VERSION="$(grep -E 'Version\s*=\s*"[^"]+"' "${ROOT_DIR}/internal/version/version.go" 2>/dev/null | head -n1 | sed -E 's/.*"([^"]+)".*/\1/' || true)"
if [ -z "${VERSION}" ]; then
  VERSION="0.5.3"
fi

GIT_COMMIT="$(git -C "${ROOT_DIR}" rev-parse --short HEAD 2>/dev/null || echo "dev")"
BUILD_DATE="$(date +%Y-%m-%d)"

echo "==> Cleaning previous release artifacts in ${DIST_DIR}..."
mkdir -p "${DIST_DIR}"
rm -f "${DIST_DIR}"/agentpulse-* "${DIST_DIR}"/SHA256SUMS

TARGETS=(
  "darwin/arm64"
  "darwin/amd64"
  "linux/arm64"
  "linux/amd64"
  "windows/amd64"
)

LDFLAGS="-s -w -X github.com/mayingjie198448/agentpulse-cli/internal/version.GitCommit=${GIT_COMMIT} -X github.com/mayingjie198448/agentpulse-cli/internal/version.BuildDate=${BUILD_DATE} -X github.com/mayingjie198448/agentpulse-cli/internal/version.Version=${VERSION}"

for target in "${TARGETS[@]}"; do
  GOOS="${target%%/*}"
  GOARCH="${target##*/}"
  EXT=""
  if [ "${GOOS}" = "windows" ]; then
    EXT=".exe"
  fi
  OUT_NAME="agentpulse-${VERSION}-${GOOS}-${GOARCH}${EXT}"
  echo "==> Building ${OUT_NAME} (${GOOS}/${GOARCH})..."
  (
    cd "${ROOT_DIR}"
    CGO_ENABLED=0 GOOS="${GOOS}" GOARCH="${GOARCH}" go build \
      -trimpath \
      -ldflags "${LDFLAGS}" \
      -o "${DIST_DIR}/${OUT_NAME}" \
      ./cmd/agentpulse
  )
done

echo "==> Generating SHA256SUMS..."
(
  cd "${DIST_DIR}"
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 agentpulse-${VERSION}-* > SHA256SUMS
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum agentpulse-${VERSION}-* > SHA256SUMS
  fi
)

echo "==> Release build complete:"
ls -lh "${DIST_DIR}"
