#!/usr/bin/env bash
# 一条原生执行道：fmt、vet、适用 go test、当前平台编译。
# 不改写源码，不交叉编译充作另一平台的原生结果，不用 continue-on-error 凑绿。
# 退出码 0 只表示本平台适用步骤都通过。NOT_RUN 项没有被记成通过。

set -u

ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
# shellcheck disable=SC1091
source "$ROOT/scripts/ci/identity.sh"

WINDOWS_ONLY_SKIP='^TestWindowsNativeFixture$'
TIMEOUT='8m'

overall=0
note_rc() {
  local name="$1"
  local rc="$2"
  say "RC name=$name value=$rc"
  if [[ -n "${ARTIFACT_DIR:-}" ]]; then
    printf '%s\n' "$rc" >"$ARTIFACT_DIR/$name.rc"
  fi
  if [[ "$rc" != "0" ]]; then
    overall=1
  fi
}

run_and_record() {
  local name="$1"
  shift
  say "COMMAND_BEGIN name=$name"
  printf 'COMMAND:' | tee -a "$ARTIFACT_DIR/transcript.txt"
  printf ' %q' "$@" | tee -a "$ARTIFACT_DIR/transcript.txt"
  printf '\n' | tee -a "$ARTIFACT_DIR/transcript.txt"
  set +e
  "$@"
  local rc=$?
  set -e
  note_rc "$name" "$rc"
  printf '%s\n' "COMMAND_END name=$name"
}

print_not_run() {
  cat <<'EOF'
NOT_RUN_CATALOG_BEGIN
item=real_agy class=privileged_or_production status=NOT_RUN detail=最小CI不启动真实agy
item=real_opencode class=privileged_or_production status=NOT_RUN detail=最小CI不启动真实OpenCode
item=production_mqtt class=privileged_or_production status=NOT_RUN detail=最小CI不连接生产Broker
item=gui_session class=privileged_or_production status=NOT_RUN detail=无交互GUI或真实TTY会话；TUI纯模型测试若在本平台编译则会随go_test运行
item=os_service_install class=privileged_or_production status=NOT_RUN detail=不安装launchd、systemd或Windows服务；darwin单测只在mac道运行且现有测试替身exec
item=TestWindowsNativeFixture class=privileged_native_appcontainer status=NOT_RUN detail=运维验收命令，不是最小CI通过项；Windows道仅跳过这一项，Linux与mac因构建标签不会编译它
item=c4_e2e_driver_main class=integration_driver status=NOT_RUN detail=不执行cmd/c4-e2e-driver主程序；该包内纯函数测试随go_test运行
item=c4_windows_driver_main class=integration_driver status=NOT_RUN detail=不执行cmd/c4-windows-driver主程序
item=c4_argv_wire_driver_main class=integration_driver status=NOT_RUN detail=不执行cmd/c4-argv-wire-driver主程序
item=cross_compile class=not_native status=NOT_RUN detail=本步不交叉编译；交叉编译结果不能当作Linux、mac或Windows原生测试
item=linux_arm64 class=no_native_runner status=NOT_RUN detail=没有linux/arm64原生runner
item=darwin_amd64 class=no_native_runner status=NOT_RUN detail=没有darwin/amd64原生runner
item=cross_repo_cpu_disk_lock class=schedule status=NOT_IMPLEMENTED detail=本仓concurrency不是跨仓锁，不能声称已错开mini或ccdq上的其它仓库
NOT_RUN_CATALOG_END
EOF
}

static_contract() {
  local failed=0
  local workflow="$ROOT/.github/workflows/minimal-ci.yml"
  local file
  for file in "$ROOT/scripts/ci/identity.sh" "$ROOT/scripts/ci/gate.sh" "$ROOT/scripts/ci/minimal-ci.sh" "$ROOT/scripts/ci/contract-selfcheck.sh" "$ROOT/scripts/ci/trust-boundary.sh" "$ROOT/scripts/ci/pathnorm.sh" "$ROOT/scripts/ci/use-go-1.27.4.sh"; do
    if ! bash -n "$file"; then
      echo "CONTRACT_FAIL syntax $file"
      failed=1
    fi
  done
  # 针脚拆开写，避免合同检查命中自己的源码行。
  local gofmt_w or_true hide_key
  gofmt_w="$(printf '%s %s' 'gofmt' '-w')"
  or_true="$(printf '%s %s' '||' 'true')"
  hide_key="$(printf '%s%s' 'continue-on-error' ':')"
  if grep -n -F -e "$gofmt_w" -e "$or_true" -e "$hide_key" \
    "$ROOT/scripts/ci/identity.sh" \
    "$ROOT/scripts/ci/gate.sh" \
    "$ROOT/scripts/ci/contract-selfcheck.sh" \
    "$ROOT/scripts/ci/minimal-ci.sh" \
    "$ROOT/scripts/ci/trust-boundary.sh" \
    "$workflow"; then
    echo "CONTRACT_FAIL hidden_success_pattern"
    failed=1
  fi
  if grep -n -E 'runs-on:[[:space:]]*self-hosted$|runs-on:[[:space:]]*\[self-hosted\]' "$workflow"; then
    echo "CONTRACT_FAIL self_hosted_only"
    failed=1
  fi
  local hosted_runner pr_event
  hosted_runner="$(printf '%s' 'ubuntu-latest')"
  pr_event="$(printf '%s_%s' 'pull' 'request')"
  if grep -n -F -e "$hosted_runner" -e 'macos-latest' -e 'windows-latest' "$workflow"; then
    echo "CONTRACT_FAIL hosted_runner"
    failed=1
  fi
  if grep -n -F -e "$pr_event" "$workflow"; then
    echo "CONTRACT_FAIL untrusted_pr_trigger"
    failed=1
  fi
  if grep -n -F 'head.sha' "$workflow"; then
    echo "CONTRACT_FAIL untrusted_head_checkout"
    failed=1
  fi
  if ! grep -F -q 'mayingjie/minimal-ci-8a30' "$workflow"; then
    echo "CONTRACT_FAIL trusted_branch_missing"
    failed=1
  fi
  if grep -n -E 'ref:[[:space:]]*9ac0b5c2ef21a9b77c501e67a8836e37a59e50bf|git fetch .*9ac0b5c2ef21a9b77c501e67a8836e37a59e50bf' "$workflow"; then
    echo "CONTRACT_FAIL still_checks_out_pr55_original"
    failed=1
  fi
  if grep -n -F 'shell: bash' "$workflow"; then
    echo "CONTRACT_FAIL ambiguous_bash_shell"
    failed=1
  fi
  if ! grep -F -q 'C:\Program Files\Git\bin\bash.exe' "$workflow"; then
    echo "CONTRACT_FAIL windows_git_bash_missing"
    failed=1
  fi
  if ! grep -F -q 'shell: powershell' "$workflow"; then
    echo "CONTRACT_FAIL windows_powershell_missing"
    failed=1
  fi
  if grep -n -F 'false &&' "$workflow"; then
    echo "CONTRACT_FAIL execution_still_disabled"
    failed=1
  fi
  if grep -n -F 'agentpulse-ci-disabled-pending-approval' "$workflow"; then
    echo "CONTRACT_FAIL disabled_branch_still_present"
    failed=1
  fi
  if grep -n -F 'actions: write' "$workflow"; then
    echo "CONTRACT_FAIL actions_write_not_required"
    failed=1
  fi
  if ! grep -F -q 'contents: read' "$workflow"; then
    echo "CONTRACT_FAIL contents_read_missing"
    failed=1
  fi
  if ! grep -F -q 'persist-credentials: false' "$workflow"; then
    echo "CONTRACT_FAIL credentials_not_disabled"
    failed=1
  fi
  if ! grep -F -q 'ref: ${{ github.sha }}' "$workflow"; then
    echo "CONTRACT_FAIL pushed_sha_checkout_missing"
    failed=1
  fi
  if grep -n -E 'workflow_dispatch|workflow_call' "$workflow"; then
    echo "CONTRACT_FAIL manual_dispatch_enabled"
    failed=1
  fi
  if grep -n -F 'secrets.' "$workflow"; then
    echo "CONTRACT_FAIL secrets_context"
    failed=1
  fi
  if ! grep -F -q '[self-hosted, macOS, ARM64, agentpulse]' "$workflow"; then
    echo "CONTRACT_FAIL mac_runs_on_missing"
    failed=1
  fi
  if ! grep -F -q '[self-hosted, Windows, X64, agentpulse]' "$workflow"; then
    echo "CONTRACT_FAIL windows_runs_on_missing"
    failed=1
  fi
  if ! grep -F -q 'self-hosted,macOS,ARM64,agentpulse' "$workflow"; then
    echo "CONTRACT_FAIL mac_label_confirmation_missing"
    failed=1
  fi
  if ! grep -F -q 'self-hosted,Windows,X64,agentpulse' "$workflow"; then
    echo "CONTRACT_FAIL windows_label_confirmation_missing"
    failed=1
  fi
  if grep -n -E 'runs-on:[[:space:]]*\[[[:space:]]*self-hosted,[[:space:]]*macOS,[[:space:]]*ARM64[[:space:]]*\]' "$workflow"; then
    echo "CONTRACT_FAIL mac_runs_on_missing_agentpulse"
    failed=1
  fi
  if grep -n -E 'runs-on:[[:space:]]*\[[[:space:]]*self-hosted,[[:space:]]*Windows,[[:space:]]*X64[[:space:]]*\]' "$workflow"; then
    echo "CONTRACT_FAIL windows_runs_on_missing_agentpulse"
    failed=1
  fi
  if ! grep -F -q "WINDOWS_ONLY_SKIP='^TestWindowsNativeFixture\$'" "$ROOT/scripts/ci/minimal-ci.sh"; then
    echo "CONTRACT_FAIL windows_skip_drifted"
    failed=1
  fi
  local skip_flag skip_hits skip_rc
  skip_flag="$(printf '%s%s' '-s' 'kip')"
  set +e
  # -e 避免把以连字符开头的针脚当成 grep 选项。
  skip_hits="$(grep -n -F -e "$skip_flag" "$ROOT/scripts/ci/minimal-ci.sh")"
  skip_rc=$?
  set +e
  if [[ "$skip_rc" -ne 0 ]]; then
    echo "CONTRACT_FAIL skip_scan_rc=$skip_rc"
    failed=1
  elif printf '%s\n' "$skip_hits" | grep -v 'WINDOWS_ONLY_SKIP' >/dev/null; then
    echo "CONTRACT_FAIL unexpected_skip"
    printf '%s\n' "$skip_hits" | grep -v 'WINDOWS_ONLY_SKIP'
    failed=1
  fi
  if [[ "$failed" != "0" ]]; then
    echo "CONTRACT_RC=1"
    return 1
  fi
  echo "CONTRACT_RC=0"
  return 0
}

if [[ "${1:-}" == "--self-check" ]]; then
  static_contract
  exit $?
fi

if [[ -z "${ARTIFACT_DIR:-}" ]]; then
  echo "ARTIFACT_DIR is required"
  exit 1
fi
mkdir -p "$ARTIFACT_DIR"
# 同时写到调用方标准输出和 transcript。不使用管道吞掉退出码。
say() {
  printf '%s\n' "$*" | tee -a "$ARTIFACT_DIR/transcript.txt"
}

cd "$ROOT"
if ! verify_identity "$ROOT" "$ARTIFACT_DIR"; then
  print_not_run
  echo "OVERALL_RC=1 reason=identity"
  exit 1
fi
SCRIPT_SHA="$(git -C "$ROOT" rev-parse HEAD)"
SCRIPT_TREE="$(git -C "$ROOT" rev-parse 'HEAD^{tree}')"
printf '%s\n' "SCRIPT_SHA=$SCRIPT_SHA"
printf '%s\n' "SCRIPT_TREE=$SCRIPT_TREE"
WORK_ROOT="$ROOT"
if [[ -n "${TARGET_ROOT:-}" ]]; then
  WORK_ROOT="$(cd "$TARGET_ROOT" && pwd)"
  TARGET_HEAD="$(git -C "$WORK_ROOT" rev-parse HEAD)"
  TARGET_TREE="$(git -C "$WORK_ROOT" rev-parse 'HEAD^{tree}')"
  printf '%s\n' "TARGET_SHA_ACTUAL=$TARGET_HEAD"
  printf '%s\n' "TARGET_TREE=$TARGET_TREE"
  if [[ "$TARGET_HEAD" == "$SCRIPT_SHA" ]]; then
    printf '%s\n' "RESULT_SCOPE=pushed_upgrade_candidate"
    printf '%s\n' "SCRIPT_SHA_EQUALS_TARGET_SHA=yes"
  else
    printf '%s\n' "RESULT_SCOPE=target_differs_from_script"
    echo "OVERALL_RC=1 reason=target_is_not_the_pushed_candidate"
    exit 1
  fi
  if [[ -z "${TARGET_SHA:-}" || "$TARGET_HEAD" != "$TARGET_SHA" ]]; then
    echo "OVERALL_RC=1 reason=target_sha_mismatch expected=${TARGET_SHA:-<unset>} actual=$TARGET_HEAD"
    exit 1
  fi
  if [[ -n "$(git -C "$WORK_ROOT" status --porcelain)" ]]; then
    echo "OVERALL_RC=1 reason=target_tree_dirty_before"
    git -C "$WORK_ROOT" status --porcelain
    exit 1
  fi
else
  printf '%s\n' "RESULT_SCOPE=script_checkout"
  printf '%s\n' "SCRIPT_SHA_EQUALS_TARGET_SHA=yes"
fi
cd "$WORK_ROOT"

set +e
go_env="$(bash "$ROOT/scripts/ci/use-go-1.27.4.sh" 2>"$ARTIFACT_DIR/go-install.stderr")"
go_env_rc=$?
set -e
cat "$ARTIFACT_DIR/go-install.stderr"
if [[ "$go_env_rc" != "0" ]]; then
  note_rc toolchain "$go_env_rc"
  echo "OVERALL_RC=1 reason=go_install_failed"
  exit 1
fi
eval "$go_env"
hash -r
set +e
go_version="$(go version 2>&1)"
go_version_rc=$?
set -e
printf '%s\n' "GO_VERSION_LINE=$go_version"
printf '%s\n' "GOROOT_LINE=$(go env GOROOT)"
printf '%s\n' "$go_version" >"$ARTIFACT_DIR/go-version.txt"
if [[ "$go_version_rc" != "0" ]]; then
  note_rc toolchain "$go_version_rc"
  print_not_run
  echo "OVERALL_RC=1 reason=go_version_command_failed"
  exit 1
fi
case "$go_version" in
  "go version go1.27.4 "*)
    note_rc toolchain 0
    ;;
  *)
    note_rc toolchain 1
    print_not_run
    echo "OVERALL_RC=1 reason=go_baseline_not_1.27.4"
    exit 1
    ;;
esac
# shellcheck disable=SC1091
source "$ROOT/scripts/ci/pathnorm.sh"
goroot="$(go env GOROOT)"
selected="${AGENTPULSE_GOROOT_SELECTED:-$GOROOT}"
if ! goroot_is_job_local "$selected" "$goroot"; then
  note_rc toolchain 1
  echo "OVERALL_RC=1 reason=refusing_non_job_go goroot=$goroot selected=$selected"
  exit 1
fi

goos="$(go env GOOS)"
goarch="$(go env GOARCH)"
printf '%s\n' "PLATFORM goos=$goos goarch=$goarch"
if [[ "${1:-}" == "--cross-compile" ]]; then
  overall=0
  printf '%s\n' "MODE=cross_compile"
  printf '%s\n' "CI_LINUX_NATIVE_TEST=NOT_RUN reason=no_linux_self_hosted_runner"
  printf '%s\n' "CROSS_COMPILE_IS_NOT_NATIVE_LINUX_TEST=yes"
  printf '%s\n' "LOCAL_HOST goos=$goos goarch=$goarch"
  mkdir -p "$ARTIFACT_DIR/cross"
  for spec in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64; do
    target_os="${spec%%/*}"
    target_arch="${spec##*/}"
    ext=""
    if [[ "$target_os" == "windows" ]]; then
      ext=".exe"
    fi
    out="$ARTIFACT_DIR/cross/agentpulse-${target_os}-${target_arch}${ext}"
    printf '%s\n' "COMMAND: CGO_ENABLED=0 GOOS=$target_os GOARCH=$target_arch go build -trimpath -o $out ./cmd/agentpulse"
    set +e
    CGO_ENABLED=0 GOOS="$target_os" GOARCH="$target_arch" go build -trimpath -o "$out" ./cmd/agentpulse
    build_rc=$?
    set -e
    printf '%s\n' "RC name=cross_${target_os}_${target_arch} value=$build_rc"
    if [[ "$target_os" == "$goos" && "$target_arch" == "$goarch" ]]; then
      printf '%s\n' "CLASS=host_arch_build_still_not_ci_linux_native_lane goos=$target_os goarch=$target_arch"
    else
      printf '%s\n' "CLASS=cross_compile_not_native_test goos=$target_os goarch=$target_arch"
    fi
    if [[ "$build_rc" != "0" ]]; then
      overall=1
    fi
  done
  if ! git -C "$WORK_ROOT" diff --quiet || [[ -n "$(git -C "$WORK_ROOT" status --porcelain)" ]]; then
    echo "TARGET_TREE_CHANGED_DURING_CROSS"
    git -C "$WORK_ROOT" status --porcelain
    overall=1
  elif ! git -C "$ROOT" diff --quiet || [[ -n "$(git -C "$ROOT" status --porcelain)" ]]; then
    echo "SCRIPT_TREE_CHANGED_DURING_CROSS"
    git -C "$ROOT" status --porcelain
    overall=1
  else
    echo "WORKTREE_UNCHANGED=0"
  fi
  echo "OVERALL_RC=$overall"
  exit "$overall"
fi
printf '%s\n' "UNAME=$(uname -srm)"
go env GOVERSION GOTOOLCHAIN GOOS GOARCH CGO_ENABLED >"$ARTIFACT_DIR/go-env.txt"
printf '%s\n' "COMMAND: go env GOVERSION GOTOOLCHAIN GOOS GOARCH CGO_ENABLED"
cat "$ARTIFACT_DIR/go-env.txt"

fmt_list="$ARTIFACT_DIR/gofmt.txt"
set +e
gofmt -l . >"$fmt_list"
fmt_tool_rc=$?
set -e
printf '%s\n' "COMMAND: gofmt -l ."
printf '%s\n' "RC name=gofmt_tool value=$fmt_tool_rc"
if [[ "$fmt_tool_rc" != "0" ]]; then
  note_rc fmt "$fmt_tool_rc"
elif [[ -s "$fmt_list" ]]; then
  echo "FMT_NONEMPTY files were listed and not rewritten"
  cat "$fmt_list"
  note_rc fmt 1
else
  note_rc fmt 0
fi

run_and_record vet go vet ./...

if [[ "$goos" == "windows" ]]; then
  if [[ -z "${RUNNER_TEMP:-}" ]]; then
    echo "WINDOWS_ABSOLUTE_TEMP_MISSING RUNNER_TEMP is required so Go sees an absolute evidence path"
    note_rc helper 1
    note_rc test 1
    note_rc build 1
    note_rc version_smoke 1
    print_not_run
    echo "OVERALL_RC=1 reason=windows_temp"
    exit 1
  fi
  work="$RUNNER_TEMP/agentpulse-minimal-ci"
  mkdir -p "$work"
  helper="$work/agentpulse-test-helper.exe"
  evidence="$work/argv-wire-evidence"
  bin="$work/agentpulse.exe"
else
  work="$(mktemp -d "${TMPDIR:-/tmp}/agentpulse-minimal-ci.XXXXXX")"
  helper="$work/agentpulse-test-helper"
  evidence="$work/argv-wire-evidence"
  bin="$work/agentpulse"
fi
mkdir -p "$evidence"
printf '%s\n' "HELPER_PATH=$helper"
printf '%s\n' "EVIDENCE_PATH=$evidence"

run_and_record helper_build env CGO_ENABLED=0 go build -trimpath -o "$helper" ./cmd/agentpulse-test-helper
printf '%s\n' "HELPER_BUILD_DOES_NOT_WRITE_TARGET_TREE=yes"
printf '%s\n' "HELPER_SOURCE=$WORK_ROOT/cmd/agentpulse-test-helper"

export AGENTPULSE_TEST_HELPER="$helper"
export AGENTPULSE_ARGV_WIRE_EVIDENCE="$evidence"
test_cmd=(go test -count=1 -timeout "$TIMEOUT")
if [[ "$goos" == "windows" ]]; then
  test_cmd+=(-skip "$WINDOWS_ONLY_SKIP")
fi
test_cmd+=(./...)
printf '%s\n' "COMMAND_BEGIN name=test"
printf 'COMMAND:'
printf ' %q' "${test_cmd[@]}"
printf '\n'
printf '%s\n' "ENV AGENTPULSE_TEST_HELPER=$AGENTPULSE_TEST_HELPER"
printf '%s\n' "ENV AGENTPULSE_ARGV_WIRE_EVIDENCE=$AGENTPULSE_ARGV_WIRE_EVIDENCE"
set +e
"${test_cmd[@]}" >"$ARTIFACT_DIR/go-test.txt" 2>&1
test_rc=$?
set -e
cat "$ARTIFACT_DIR/go-test.txt"
note_rc test "$test_rc"
printf '%s\n' "COMMAND_END name=test"

run_and_record build env CGO_ENABLED=0 go build -trimpath -o "$bin" ./cmd/agentpulse
if [[ -x "$bin" || -f "$bin" ]]; then
  run_and_record version_smoke "$bin" version
  sha256sum "$bin" >"$ARTIFACT_DIR/binary.sha256" 2>/dev/null || shasum -a 256 "$bin" >"$ARTIFACT_DIR/binary.sha256"
  cat "$ARTIFACT_DIR/binary.sha256"
else
  note_rc version_smoke 1
fi

if ! git -C "$WORK_ROOT" diff --quiet || [[ -n "$(git -C "$WORK_ROOT" status --porcelain)" ]]; then
  echo "TARGET_TREE_CHANGED_DURING_CI"
  git -C "$WORK_ROOT" status --porcelain
  note_rc worktree_unchanged 1
elif ! git -C "$ROOT" diff --quiet || [[ -n "$(git -C "$ROOT" status --porcelain)" ]]; then
  echo "SCRIPT_TREE_CHANGED_DURING_CI"
  git -C "$ROOT" status --porcelain
  note_rc worktree_unchanged 1
else
  note_rc worktree_unchanged 0
fi

print_not_run
if [[ "$goos" != "windows" ]]; then
  echo "NOT_RUN item=windows_build_tagged_tests status=NOT_RUN detail=not_compiled_on_$goos"
fi
if [[ "$goos" != "darwin" ]]; then
  echo "NOT_RUN item=darwin_build_tagged_tests status=NOT_RUN detail=not_compiled_on_$goos"
fi
if [[ "$goos" != "linux" ]]; then
  echo "NOT_RUN item=linux_build_tagged_tests status=NOT_RUN detail=not_compiled_on_$goos"
fi
echo "NATIVE_COMPILE goos=$goos goarch=$goarch cgo=0 package=./cmd/agentpulse"
echo "OVERALL_RC=$overall"
exit "$overall"
