#!/usr/bin/env bash
set -euo pipefail

# scripts/build-macos-app.sh
# Packages AgentPulse CLI into a standalone macOS Apple Silicon (arm64) .app bundle.

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

DIST_DIR="${ROOT_DIR}/dist"
APP_NAME="AgentPulse.app"
APP_DIR="${DIST_DIR}/${APP_NAME}"
CONTENTS_DIR="${APP_DIR}/Contents"
MACOS_DIR="${CONTENTS_DIR}/MacOS"
RESOURCES_DIR="${CONTENTS_DIR}/Resources"

# Read version from Go source without modification (defaults to 0.2.0 if unset)
VERSION="$(grep -E 'Version\s*=\s*"[^"]+"' "${ROOT_DIR}/internal/version/version.go" 2>/dev/null | head -n1 | sed -E 's/.*"([^"]+)".*/\1/' || true)"
if [ -z "${VERSION}" ]; then
  VERSION="0.2.0"
fi

GIT_COMMIT="$(git -C "${ROOT_DIR}" rev-parse --short HEAD 2>/dev/null || echo "dev")"
BUILD_DATE="$(date +%Y-%m-%d)"

echo "==> Cleaning previous bundle at ${APP_DIR}..."
rm -rf "${APP_DIR}"
mkdir -p "${MACOS_DIR}" "${RESOURCES_DIR}"

echo "==> Building darwin/arm64 Go binary (v${VERSION}, commit ${GIT_COMMIT})..."
(
  cd "${ROOT_DIR}"
  CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build \
    -trimpath \
    -ldflags "-s -w -X github.com/mayingjie198448/agentpulse-cli/internal/version.GitCommit=${GIT_COMMIT} -X github.com/mayingjie198448/agentpulse-cli/internal/version.BuildDate=${BUILD_DATE}" \
    -o "${MACOS_DIR}/agentpulse-bin" \
    ./cmd/agentpulse
)
chmod +x "${MACOS_DIR}/agentpulse-bin"

echo "==> Creating launcher script..."
cat <<'EOF' > "${MACOS_DIR}/agentpulse"
#!/usr/bin/env bash
set -euo pipefail

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN="${DIR}/agentpulse-bin"

# If arguments were provided or stdin/stdout are attached to a terminal, execute directly
if [ "$#" -gt 0 ] || [ -t 0 ] || [ -t 1 ]; then
  exec "${BIN}" "$@"
fi

# Double-clicked from Finder (no attached TTY):
# Launch Terminal.app to show version and help, then keep interactive shell alive
LAUNCH_CMD="\"${BIN}\" version && echo '' && \"${BIN}\" --help && echo '' && echo 'AgentPulse interactive session ready.' && exec \${SHELL:-/bin/zsh}"

if command -v osascript >/dev/null 2>&1; then
  osascript -e "tell application \"Terminal\" to activate" \
            -e "tell application \"Terminal\" to do script \"${LAUNCH_CMD}\"" >/dev/null 2>&1 || exec "${BIN}" "$@"
else
  open -a Terminal "${BIN}" || exec "${BIN}" "$@"
fi
EOF
chmod +x "${MACOS_DIR}/agentpulse"

echo "==> Writing Contents/Info.plist..."
cat <<EOF > "${CONTENTS_DIR}/Info.plist"
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleDevelopmentRegion</key>
    <string>en</string>
    <key>CFBundleDisplayName</key>
    <string>AgentPulse</string>
    <key>CFBundleExecutable</key>
    <string>agentpulse</string>
    <key>CFBundleIdentifier</key>
    <string>com.mayingjie.agentpulse</string>
    <key>CFBundleInfoDictionaryVersion</key>
    <string>6.0</string>
    <key>CFBundleName</key>
    <string>AgentPulse</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleShortVersionString</key>
    <string>${VERSION}</string>
    <key>CFBundleVersion</key>
    <string>${VERSION}</string>
    <key>LSMinimumSystemVersion</key>
    <string>12.0</string>
    <key>LSArchitecturePriority</key>
    <array>
        <string>arm64</string>
    </array>
    <key>NSHighResolutionCapable</key>
    <true/>
</dict>
</plist>
EOF

echo "==> Creating Contents/Resources placeholder..."
cat <<'EOF' > "${RESOURCES_DIR}/README.txt"
AgentPulse macOS Application Bundle (Apple Silicon arm64)
EOF

# Optional ad-hoc codesigning placeholder (no paid Apple Developer certificate required):
# codesign --force --deep --sign - "${APP_DIR}"

echo ""
echo "==> Successfully created: ${APP_DIR}"
echo ""
echo "Binary architecture check:"
file "${MACOS_DIR}/agentpulse-bin"
if command -v lipo >/dev/null 2>&1; then
  lipo -info "${MACOS_DIR}/agentpulse-bin"
fi
echo ""
echo "Verification commands:"
echo "  ${APP_DIR}/Contents/MacOS/agentpulse version"
echo "  open ${APP_DIR}"
