#!/usr/bin/env bash
# 准入门禁。不编译、不测试、不注册 runner、不派发 self-hosted 作业。
# 退出码：0 三条执行道都 READY；2 至少一条 BLOCKED；1 身份或脚本错误。
# BLOCKED 不是通过。调用方不得把退出码 2 改写成成功。

set -u

# 已确认的本仓 runner 标签。第四个 agentpulse 使本仓作业不会落到
# ccdq 的三标签 runner 上。注册或 offline 都不是 ready。
# mac: agentpulse-mac-mini id 3，登记时 offline，服务未安装/未启用。
# windows: agentpulse-ccdq-win id 2，登记时 offline，服务未安装/未启用。
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
  if [[ "${AGENTPULSE_CI_LINUX_READY:-}" == "ready" ]]; then
    printf '%s\n' "READY reason=AGENTPULSE_CI_LINUX_READY=ready hosted_public_linux_not_a_self_hosted_machine"
  else
    printf '%s\n' "BLOCKED reason=AGENTPULSE_CI_LINUX_READY_is_not_exactly_ready value=${AGENTPULSE_CI_LINUX_READY:-<unset>}"
  fi
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
  printf '%s\n' "RESIDUAL_RISK=once_online_other_repo_jobs_that_require_only_the_three_os_labels_can_still_select_these_runners"
  printf '%s\n' "OTHER_REPOS_AND_HOST_MUTEX=unchanged_pending_separate_approval"
  if [[ "$linux_decision" == READY* && "$darwin_decision" == READY* && "$windows_decision" == READY* && "${AGENTPULSE_CI_QUEUE_AUTHORIZED:-}" == "ready" ]]; then
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
      echo "- 跨仓 CPU/磁盘互斥：未实现。本仓 concurrency 不能错开同一台机器上其它仓库的 runner。"
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
