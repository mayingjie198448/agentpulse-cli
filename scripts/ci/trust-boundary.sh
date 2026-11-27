#!/usr/bin/env bash
# 可信同仓 push 入口。允许列表只有本仓库的 CI 分支。
# 被测树就是这次 push 的 github.sha，与脚本检出是同一棵派生升级候选。
# 它不是 PR55 原提交。不执行 fork，不执行拉取请求里的代码。

set -u

REPOSITORY_EXPECTED='mayingjie198448/agentpulse-cli'
REF_EXPECTED='refs/heads/mayingjie/minimal-ci-8a30'
PR55_ORIGINAL='9ac0b5c2ef21a9b77c501e67a8836e37a59e50bf'

evaluate_trust() {
  local event="$1"
  local repository="$2"
  local ref="$3"
  local script_sha="$4"
  local script_head="$5"
  local target_sha="$6"
  local target_head="$7"
  local fork="$8"
  printf '%s\n' "TRUST_EVENT=$event"
  printf '%s\n' "TRUST_REPOSITORY=$repository"
  printf '%s\n' "TRUST_REF=$ref"
  printf '%s\n' "TRUST_SCRIPT_SHA=$script_sha"
  printf '%s\n' "TRUST_SCRIPT_HEAD=$script_head"
  printf '%s\n' "TRUST_TARGET_SHA=$target_sha"
  printf '%s\n' "TRUST_TARGET_HEAD=$target_head"
  printf '%s\n' "TRUST_FORK=$fork"
  printf '%s\n' "PR55_ORIGINAL=$PR55_ORIGINAL"
  printf '%s\n' "RESULT_IS_NOT_PR55_ORIGINAL=yes"
  printf '%s\n' "FORK_POLICY=fork_code_is_not_executed_on_self_hosted"
  printf '%s\n' "CHECKOUT_POLICY=github_sha_of_this_push_is_the_upgrade_candidate"
  printf '%s\n' "SECRET_BOUNDARY=no_secrets_context permissions_contents_read credentials_not_persisted"
  printf '%s\n' "EXECUTES_UNTRUSTED_HEAD=no"
  if [[ "$event" == "pull_request_target" ]]; then
    printf '%s\n' "TRUST_RESULT=REFUSED reason=would_check_out_untrusted_head_from_trusted_workflow"
    return 1
  fi
  if [[ "$event" == "pull_request" || "$fork" == "true" ]]; then
    printf '%s\n' "TRUST_RESULT=BLOCKED reason=fork_or_pull_request_code_is_not_executed"
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
  if [[ "$ref" != "$REF_EXPECTED" ]]; then
    printf '%s\n' "TRUST_RESULT=BLOCKED reason=ref_not_in_allowlist ref=$ref"
    return 2
  fi
  if [[ -z "$script_sha" || "$script_sha" != "$script_head" ]]; then
    printf '%s\n' "TRUST_RESULT=BLOCKED reason=script_checkout_is_not_the_pushed_sha"
    return 2
  fi
  if [[ -z "$target_head" || "$target_sha" != "$script_sha" || "$target_head" != "$script_sha" ]]; then
    printf '%s\n' "TRUST_RESULT=BLOCKED reason=target_is_not_the_pushed_upgrade_candidate"
    return 2
  fi
  if [[ "$target_sha" == "$PR55_ORIGINAL" ]]; then
    printf '%s\n' "TRUST_RESULT=BLOCKED reason=pr55_original_still_has_go_1_27_3"
    return 2
  fi
  if [[ "$fork" != "false" ]]; then
    printf '%s\n' "TRUST_RESULT=BLOCKED reason=fork_flag_not_false"
    return 2
  fi
  printf '%s\n' "TRUST_RESULT=ALLOW reason=same_repo_push_of_upgrade_candidate"
  return 0
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
    if [[ "$expected_rc" == "0" && "$output" != *"TRUST_RESULT=ALLOW"* ]]; then
      echo "CASE_FAIL $name missing_ALLOW"
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
  check_case allowed_ci_push 0 push "$REPOSITORY_EXPECTED" "$REF_EXPECTED" abc abc abc abc false
  check_case untrusted_target 1 pull_request_target "$REPOSITORY_EXPECTED" "$REF_EXPECTED" abc abc abc abc false
  check_case pull_request_not_executed 2 pull_request "$REPOSITORY_EXPECTED" "$REF_EXPECTED" abc abc abc abc false
  check_case fork_not_executed 2 push "$REPOSITORY_EXPECTED" "$REF_EXPECTED" abc abc abc abc true
  check_case wrong_repository 2 push example/other "$REF_EXPECTED" abc abc abc abc false
  check_case wrong_ref 2 push "$REPOSITORY_EXPECTED" refs/heads/main abc abc abc abc false
  check_case script_sha_mismatch 2 push "$REPOSITORY_EXPECTED" "$REF_EXPECTED" abc def abc abc false
  check_case original_pr55_not_the_candidate 2 push "$REPOSITORY_EXPECTED" "$REF_EXPECTED" "$PR55_ORIGINAL" "$PR55_ORIGINAL" "$PR55_ORIGINAL" "$PR55_ORIGINAL" false
  check_case missing_fork_flag 2 push "$REPOSITORY_EXPECTED" "$REF_EXPECTED" abc abc abc abc ""
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
script_sha="${GITHUB_SHA:-}"
if [[ -n "${EXPECTED_SHA:-}" ]]; then
  script_sha="$EXPECTED_SHA"
fi
script_head=""
if git rev-parse HEAD >/dev/null 2>&1; then
  script_head="$(git rev-parse HEAD)"
fi
target_sha="${TARGET_SHA:-}"
target_head=""
if [[ -n "${TARGET_ROOT:-}" ]] && git -C "$TARGET_ROOT" rev-parse HEAD >/dev/null 2>&1; then
  target_head="$(git -C "$TARGET_ROOT" rev-parse HEAD)"
fi
evaluate_trust "$event" "$repository" "$ref" "$script_sha" "$script_head" "$target_sha" "$target_head" "${FORK:-}"
exit $?
