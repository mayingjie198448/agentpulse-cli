#!/usr/bin/env bash
# 准入门禁。不编译、不测试、不注册 runner、不派发 self-hosted 作业。
# 退出码：0 三条执行道都 READY；2 至少一条 BLOCKED；1 身份或脚本错误。
# BLOCKED 不是通过。调用方不得把退出码 2 改写成成功。

set -u

# 本仓仓库级 runner。它们只绑定 agentpulse-cli，不能被 ccdq-go 选中。
# ccdq-go 的 runner 也只绑定那个仓库，不能被本仓选中。
# 第四个标签 agentpulse 只用于本仓两台机器之间的精确选择，不是跨仓防护。
# 两套仓库级实例仍可能共享同一台物理机。主机锁是另一项工作，本脚本不启用它。
# mac: agentpulse-mac-mini id 3，登记时 offline，服务未安装/未启用。
# windows: agentpulse-ccdq-win id 2，登记时 offline，服务未安装/未启用。
# 注册或 offline 都不是 ready。没有 Linux 自托管 runner，Linux 原生测试保持 NOT_RUN。
MAC_EXPECTED='self-hosted,macOS,ARM64,agentpulse'
WIN_EXPECTED='self-hosted,Windows,X64,agentpulse'

forbidden_label_set() {
  case "$1" in
    self-hosted | self-hosted,macOS,ARM64 | self-hosted,Windows,X64)
      return 0
      ;;
    *)
      return 1
      ;;
  esac
}

decide_linux() {
  printf '%s\n' "NOT_RUN reason=no_linux_self_hosted_runner cross_compile_is_not_native_linux_test linux_ready_var=${AGENTPULSE_CI_LINUX_READY:-<unset>} ignored=yes"
}

decide_machine() {
  local expected="$1"
  local actual="$2"
  if [[ "${AGENTPULSE_CI_RUNNER_READY:-}" != "ready" ]]; then
    printf '%s\n' "BLOCKED reason=AGENTPULSE_CI_RUNNER_READY_is_not_exactly_ready value=${AGENTPULSE_CI_RUNNER_READY:-<unset>}"
    return 0
  fi
  if [[ "${AGENTPULSE_CI_SCHEDULE_READY:-}" != "ready" ]]; then
    printf '%s\n' "BLOCKED reason=AGENTPULSE_CI_SCHEDULE_READY_is_not_exactly_ready cross_repo_lock_not_confirmed value=${AGENTPULSE_CI_SCHEDULE_READY:-<unset>}"
    return 0
  fi
  if [[ -z "$actual" ]]; then
    printf '%s\n' "BLOCKED reason=label_confirmation_unset expected=$expected"
    return 0
  fi
  if forbidden_label_set "$actual"; then
    printf '%s\n' "BLOCKED reason=FORBIDDEN_LABELS set=$actual"
    return 0
  fi
  if [[ "$actual" != "$expected" ]]; then
    printf '%s\n' "BLOCKED reason=label_confirmation_mismatch expected=$expected actual=$actual"
    return 0
  fi
  printf '%s\n' "READY reason=runner_schedule_and_unique_labels_confirmed labels=$expected"
}

emit_decisions() {
  local linux_decision darwin_decision windows_decision overall
  linux_decision="$(decide_linux)"
  darwin_decision="$(decide_machine "$MAC_EXPECTED" "${AGENTPULSE_MAC_RUNNER_LABELS:-}")"
  windows_decision="$(decide_machine "$WIN_EXPECTED" "${AGENTPULSE_WINDOWS_RUNNER_LABELS:-}")"
  printf '%s\n' "LANE linux $linux_decision"
  printf '%s\n' "LANE darwin $darwin_decision"
  printf '%s\n' "LANE windows $windows_decision"
  printf '%s\n' "CROSS_REPO_LOCK=NOT_IMPLEMENTED single_repo_concurrency_is_not_a_lock"
  printf '%s\n' "MAC_RUNS_ON=$MAC_EXPECTED"
  printf '%s\n' "WINDOWS_RUNS_ON=$WIN_EXPECTED"
  printf '%s\n' "CCDQ_MAC_LABELS_FORBIDDEN=self-hosted,macOS,ARM64"
  printf '%s\n' "CCDQ_WINDOWS_LABELS_FORBIDDEN=self-hosted,Windows,X64"
  printf '%s\n' "RECORDED_AT_AUTHORING runner=agentpulse-mac-mini id=3 runs_on=$MAC_EXPECTED registered=yes online=no service=not_installed_or_not_enabled"
  printf '%s\n' "RECORDED_AT_AUTHORING runner=agentpulse-ccdq-win id=2 runs_on=$WIN_EXPECTED registered=yes online=no service=not_installed_or_not_enabled"
  printf '%s\n' "RUNNER_STATUS_NOT_POLLED=yes"
  printf '%s\n' "REGISTERED_IS_NOT_READY=yes"
  printf '%s\n' "OFFLINE_IS_NOT_READY=yes"
  printf '%s\n' "SKIPPED_JOB_IS_NOT_PASS=yes"
  printf '%s\n' "QUEUE_AUTHORIZED value=${AGENTPULSE_CI_QUEUE_AUTHORIZED:-<unset>}"
  printf '%s\n' "RUNNER_BINDING=repository_scoped agentpulse-cli"
  printf '%s\n' "CCDQ_GO_RUNNERS_CANNOT_BE_SELECTED_BY_THIS_REPO=yes"
  printf '%s\n' "THIS_REPO_RUNNERS_CANNOT_BE_SELECTED_BY_OTHER_REPOS=yes"
  printf '%s\n' "AGENTPULSE_LABEL=in_repo_machine_selector_not_cross_repo_control"
  printf '%s\n' "HOST_CONTENTION=same_physical_host_is_a_separate_problem"
  printf '%s\n' "HOST_LOCK=separately_authorized_for_configuration_and_verification_not_enabled_here"
  printf '%s\n' "GITHUB_HOSTED_RUNNERS=removed"
  printf '%s\n' "LINUX_NATIVE=NOT_RUN"
  printf '%s\n' "CROSS_COMPILE_IS_NOT_NATIVE_LINUX_TEST=yes"
  if [[ "$darwin_decision" == READY* && "$windows_decision" == READY* && "${AGENTPULSE_CI_QUEUE_AUTHORIZED:-}" == "ready" ]]; then
    overall="READY"
  else
    overall="BLOCKED"
  fi
  printf '%s\n' "ADMISSION_RESULT=$overall"
  if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
    {
      echo "## minimal-ci admission"
      echo
      echo '此作业不是测试通过，也不是运维 PASS。'
      echo
      echo "- \`$linux_decision\`（linux）"
      echo "- \`$darwin_decision\`（darwin）"
      echo "- \`$windows_decision\`（windows）"
      echo "- 本仓 runner 与 ccdq-go 的 runner 都是仓库级绑定，不能互相调度。agentpulse 标签只在本仓内选机器。"
      echo "- 同一台物理机的资源争用是另一问题。主机锁已另行批准做配置和验证，这里没有启用，也不表示 CI 可以上线。"
      echo "- Linux 原生测试 NOT_RUN。交叉编译不是 Linux 原生验证。"
      echo "- 登记事实不是就绪：agentpulse-mac-mini（id 3）与 agentpulse-ccdq-win（id 2）登记时为 offline，服务未安装或未启用。本脚本不探测在线状态。"
      echo "- skipped 的执行道不是通过。"
      echo "- 结果：\`$overall\`"
    } >>"$GITHUB_STEP_SUMMARY"
  fi
  if [[ "$overall" == "READY" ]]; then
    return 0
  fi
  return 2
}

run_self_check() {
  local failed=0
  local name expected_rc output rc
  check_case() {
    name="$1"
    expected_rc="$2"
    shift 2
    set +e
    output="$(env \
      -u AGENTPULSE_CI_LINUX_READY \
      -u AGENTPULSE_CI_RUNNER_READY \
      -u AGENTPULSE_CI_SCHEDULE_READY \
      -u AGENTPULSE_MAC_RUNNER_LABELS \
      -u AGENTPULSE_WINDOWS_RUNNER_LABELS \
      -u AGENTPULSE_CI_QUEUE_AUTHORIZED \
      -u GITHUB_STEP_SUMMARY \
      -u ARTIFACT_DIR \
      "$@" \
      bash "$0" --decide-only 2>&1)"
    rc=$?
    set -e
    printf '%s\n' "CASE name=$name expected_rc=$expected_rc actual_rc=$rc"
    printf '%s\n' "$output"
    if [[ "$rc" != "$expected_rc" ]]; then
      echo "CASE_FAIL $name rc"
      failed=1
    fi
    if [[ "$expected_rc" == "0" && "$output" != *"ADMISSION_RESULT=READY"* ]]; then
      echo "CASE_FAIL $name missing_READY"
      failed=1
    fi
    if [[ "$expected_rc" == "2" && "$output" != *"ADMISSION_RESULT=BLOCKED"* ]]; then
      echo "CASE_FAIL $name missing_BLOCKED"
      failed=1
    fi
  }

  set -e
  check_case unset_all 2
  check_case linux_only 2 AGENTPULSE_CI_LINUX_READY=ready
  check_case ready_word_wrong_case 2 \
    AGENTPULSE_CI_LINUX_READY=Ready \
    AGENTPULSE_CI_RUNNER_READY=ready \
    AGENTPULSE_CI_SCHEDULE_READY=ready \
    AGENTPULSE_MAC_RUNNER_LABELS="$MAC_EXPECTED" \
    AGENTPULSE_WINDOWS_RUNNER_LABELS="$WIN_EXPECTED"
  check_case self_hosted_only 2 \
    AGENTPULSE_CI_LINUX_READY=ready \
    AGENTPULSE_CI_RUNNER_READY=ready \
    AGENTPULSE_CI_SCHEDULE_READY=ready \
    AGENTPULSE_MAC_RUNNER_LABELS='self-hosted' \
    AGENTPULSE_WINDOWS_RUNNER_LABELS="$WIN_EXPECTED"
  check_case borrowed_ccdq_mac 2 \
    AGENTPULSE_CI_LINUX_READY=ready \
    AGENTPULSE_CI_RUNNER_READY=ready \
    AGENTPULSE_CI_SCHEDULE_READY=ready \
    AGENTPULSE_MAC_RUNNER_LABELS='self-hosted,macOS,ARM64' \
    AGENTPULSE_WINDOWS_RUNNER_LABELS="$WIN_EXPECTED"
  check_case borrowed_ccdq_windows 2 \
    AGENTPULSE_CI_LINUX_READY=ready \
    AGENTPULSE_CI_RUNNER_READY=ready \
    AGENTPULSE_CI_SCHEDULE_READY=ready \
    AGENTPULSE_MAC_RUNNER_LABELS="$MAC_EXPECTED" \
    AGENTPULSE_WINDOWS_RUNNER_LABELS='self-hosted,Windows,X64'
  check_case schedule_missing 2 \
    AGENTPULSE_CI_LINUX_READY=ready \
    AGENTPULSE_CI_RUNNER_READY=ready \
    AGENTPULSE_MAC_RUNNER_LABELS="$MAC_EXPECTED" \
    AGENTPULSE_WINDOWS_RUNNER_LABELS="$WIN_EXPECTED"
  check_case runner_missing 2 \
    AGENTPULSE_CI_LINUX_READY=ready \
    AGENTPULSE_CI_SCHEDULE_READY=ready \
    AGENTPULSE_MAC_RUNNER_LABELS="$MAC_EXPECTED" \
    AGENTPULSE_WINDOWS_RUNNER_LABELS="$WIN_EXPECTED"
  check_case labels_swap 2 \
    AGENTPULSE_CI_LINUX_READY=ready \
    AGENTPULSE_CI_RUNNER_READY=ready \
    AGENTPULSE_CI_SCHEDULE_READY=ready \
    AGENTPULSE_MAC_RUNNER_LABELS="$WIN_EXPECTED" \
    AGENTPULSE_WINDOWS_RUNNER_LABELS="$MAC_EXPECTED"
  check_case registered_is_not_ready 2 \
    AGENTPULSE_CI_LINUX_READY=ready \
    AGENTPULSE_CI_RUNNER_READY=registered \
    AGENTPULSE_CI_SCHEDULE_READY=ready \
    AGENTPULSE_MAC_RUNNER_LABELS="$MAC_EXPECTED" \
    AGENTPULSE_WINDOWS_RUNNER_LABELS="$WIN_EXPECTED"
  check_case offline_is_not_ready 2 \
    AGENTPULSE_CI_LINUX_READY=ready \
    AGENTPULSE_CI_RUNNER_READY=offline \
    AGENTPULSE_CI_SCHEDULE_READY=ready \
    AGENTPULSE_MAC_RUNNER_LABELS="$MAC_EXPECTED" \
    AGENTPULSE_WINDOWS_RUNNER_LABELS="$WIN_EXPECTED"
  check_case lanes_without_queue_authorization 2 \
    AGENTPULSE_CI_LINUX_READY=ready \
    AGENTPULSE_CI_RUNNER_READY=ready \
    AGENTPULSE_CI_SCHEDULE_READY=ready \
    AGENTPULSE_MAC_RUNNER_LABELS="$MAC_EXPECTED" \
    AGENTPULSE_WINDOWS_RUNNER_LABELS="$WIN_EXPECTED"
  check_case all_confirmed 0 \
    AGENTPULSE_CI_LINUX_READY=ready \
    AGENTPULSE_CI_RUNNER_READY=ready \
    AGENTPULSE_CI_SCHEDULE_READY=ready \
    AGENTPULSE_CI_QUEUE_AUTHORIZED=ready \
    AGENTPULSE_MAC_RUNNER_LABELS="$MAC_EXPECTED" \
    AGENTPULSE_WINDOWS_RUNNER_LABELS="$WIN_EXPECTED"

  if [[ "$failed" != "0" ]]; then
    echo "GATE_SELF_CHECK_RC=1"
    return 1
  fi
  echo "GATE_SELF_CHECK_RC=0"
  return 0
}

if [[ "${1:-}" == "--decide-only" ]]; then
  emit_decisions
  exit $?
fi

if [[ "${1:-}" == "--self-check" ]]; then
  run_self_check
  exit $?
fi

root="$(cd "$(dirname "$0")/../.." && pwd)"
# shellcheck disable=SC1091
source "$root/scripts/ci/identity.sh"
if ! verify_identity "$root" "${ARTIFACT_DIR:-}"; then
  exit 1
fi
emit_decisions
exit $?
