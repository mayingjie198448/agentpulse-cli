#!/usr/bin/env bash
# Build AgentPulse.app and upload AgentPulse-<ver>-darwin-arm64.zip to a GitHub Release.
# Usage: bash scripts/release-macos-app.sh <tag>
# Example: bash scripts/release-macos-app.sh v0.4.0
# Requires: Apple Silicon Mac, Go, gh (authenticated), ditto. No secrets; repo only.
set -euo pipefail

TAG="${1:-}"
if [[ -z "${TAG}" ]]; then
  echo "usage: $0 <tag>" >&2
  exit 2
fi

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
REPO="${GITHUB_REPOSITORY:-mayingjie198448/agentpulse-cli}"

cd "${ROOT_DIR}"

VERSION="$(grep -E 'Version\s*=\s*"[^"]+"' "${ROOT_DIR}/internal/version/version.go" | head -n1 | sed -E 's/.*"([^"]+)".*/\1/')"
if [[ -z "${VERSION}" ]]; then
  echo "error: could not read Version from internal/version/version.go" >&2
  exit 1
fi

EXPECTED_TAG="v${VERSION}"
if [[ "${TAG}" != "${EXPECTED_TAG}" && "${TAG}" != "${VERSION}" ]]; then
  echo "warning: tag '${TAG}' does not match Version '${VERSION}' in source; continuing anyway" >&2
fi

bash "${SCRIPT_DIR}/build-macos-app.sh"

DIST_DIR="${ROOT_DIR}/dist"
APP_DIR="${DIST_DIR}/AgentPulse.app"
ZIP_NAME="AgentPulse-${VERSION}-darwin-arm64.zip"
ZIP_PATH="${DIST_DIR}/${ZIP_NAME}"

"${APP_DIR}/Contents/MacOS/agentpulse" version
file "${APP_DIR}/Contents/MacOS/agentpulse-bin"
lipo -info "${APP_DIR}/Contents/MacOS/agentpulse-bin"

rm -f "${ZIP_PATH}"
(
  cd "${DIST_DIR}"
  ditto -c -k --keepParent AgentPulse.app "${ZIP_NAME}"
)

echo "==> Uploading ${ZIP_NAME} to release ${TAG} (${REPO})..."
gh release upload "${TAG}" "${ZIP_PATH}" --repo "${REPO}" --clobber
gh release view "${TAG}" --repo "${REPO}"
echo "==> Done: https://github.com/${REPO}/releases/download/${TAG}/${ZIP_NAME}"
