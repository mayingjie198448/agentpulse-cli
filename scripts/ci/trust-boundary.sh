#!/usr/bin/env bash
# 可信执行边界。不检出代码，不读取密钥，不注册 runner。
# 生产允许列表为空：任何真实调用都是 BLOCKED，退出码 2。
# 拒绝在可信工作流里检出不可信头。那一类事件直接退出 1。

set -u

REPOSITORY_EXPECTED='mayingjie198448/agentpulse-cli'

evaluate_trust() {
  local event="$1"
  local repository="$2"
  local ref="$3"
  local sha="$4"
  local head_sha="$5"
  local queue="$6"
  printf '%s\n' "TRUST_EVENT=$event"
  printf '%s\n' "TRUST_REPOSITORY=$repository"
  printf '%s\n' "TRUST_REF=$ref"
  printf '%s\n' "TRUST_SHA=$sha"
  printf '%s\n' "TRUST_HEAD_SHA=$head_sha"
  printf '%s\n' "TRUST_ALLOWLIST=empty"
  printf '%s\n' "FORK_POLICY=fork_and_same_repo_pull_requests_are_not_executed"
  printf '%s\n' "CHECKOUT_POLICY=only_the_sha_of_a_push_to_this_repository"
  printf '%s\n' "SECRET_BOUNDARY=no_secrets_context permissions_contents_read credentials_not_persisted"
  printf '%s\n' "EXECUTES_UNTRUSTED_HEAD=no"
  printf '%s\n' "TRUST_QUEUE=$queue"
  printf '%s\n' "TRUST_QUEUE_DOES_NOT_OVERRIDE_EMPTY_ALLOWLIST=yes"
  if [[ "$event" == "pull_request_target" ]]; then
    printf '%s\n' "TRUST_RESULT=REFUSED reason=would_check_out_untrusted_head_from_trusted_workflow"
    return 1
  fi
  if [[ "$event" == "pull_request" ]]; then
    printf '%s\n' "TRUST_RESULT=BLOCKED reason=pull_request_code_is_not_executed"
    return 2
  fi
  if [[ "$repository" != "$REPOSITORY_EXPECTED" ]]; then
    printf '%s\n' "TRUST_RESULT=BLOCKED reason=repository_mismatch"
    return 2
  fi
  if [[ "$event" != "push" ]]; then
    printf '%s\n' "TRUST_RESULT=BLOCKED reason=event_is_not_push"
    return 2
  fi
  # 允许列表是字面空串。没有分支被授权，包括当前禁用用的占位分支名。
  printf '%s\n' "TRUST_RESULT=BLOCKED reason=trusted_ref_allowlist_empty ref=$ref"
  return 2
}

run_self_check() {
  local failed=0
  check_case() {
    local name="$1"
    local expected_rc="$2"
    shift 2
    local output rc
    set +e
    output="$(evaluate_trust "$@")"
    rc=$?
    set +e
    printf '%s\n' "CASE name=$name expected_rc=$expected_rc actual_rc=$rc"
    printf '%s\n' "$output"
    if [[ "$rc" != "$expected_rc" ]]; then
      echo "CASE_FAIL $name rc"
      failed=1
    fi
    if [[ "$expected_rc" == "2" && "$output" != *"TRUST_RESULT=BLOCKED"* ]]; then
      echo "CASE_FAIL $name missing_BLOCKED"
      failed=1
    fi
    if [[ "$expected_rc" == "1" && "$output" != *"TRUST_RESULT=REFUSED"* ]]; then
      echo "CASE_FAIL $name missing_REFUSED"
      failed=1
    fi
  }
  check_case untrusted_target 1 pull_request_target "$REPOSITORY_EXPECTED" refs/heads/main abc abc ready
  check_case pull_request_not_executed 2 pull_request "$REPOSITORY_EXPECTED" refs/pull/56/merge abc def ready
  check_case wrong_repository 2 push example/other refs/heads/main abc abc ready
  check_case missing_event 2 "" "$REPOSITORY_EXPECTED" refs/heads/main abc abc ready
  check_case push_allowlist_empty 2 push "$REPOSITORY_EXPECTED" refs/heads/agentpulse-ci-disabled-pending-approval abc abc ready
  check_case sha_mismatch_still_blocked 2 push "$REPOSITORY_EXPECTED" refs/heads/main abc def ready
  check_case queue_ready_still_blocked 2 push "$REPOSITORY_EXPECTED" refs/heads/main abc abc ready
  if [[ "$failed" != "0" ]]; then
    echo "TRUST_SELF_CHECK_RC=1"
    return 1
  fi
  echo "TRUST_SELF_CHECK_RC=0"
  return 0
}

if [[ "${1:-}" == "--self-check" ]]; then
  run_self_check
  exit $?
fi

event="${GITHUB_EVENT_NAME:-}"
repository="${GITHUB_REPOSITORY:-}"
ref="${GITHUB_REF:-}"
sha="${GITHUB_SHA:-}"
head_sha=""
if git rev-parse HEAD >/dev/null 2>&1; then
  head_sha="$(git rev-parse HEAD)"
fi
if [[ -n "${EXPECTED_SHA:-}" ]]; then
  sha="$EXPECTED_SHA"
fi
evaluate_trust "$event" "$repository" "$ref" "$sha" "$head_sha" "${AGENTPULSE_CI_QUEUE_AUTHORIZED:-}"
exit $?
