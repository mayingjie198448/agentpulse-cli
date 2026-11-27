#!/usr/bin/env bash
# 固定本次作业看到的源码身份。只读 git，不改工作区、不改写历史。
# 被其它脚本 source。直接执行时只做一次校验。

verify_identity() {
  local root="$1"
  local artifact_dir="${2:-}"
  local head tree porcelain
  head="$(git -C "$root" rev-parse HEAD)"
  tree="$(git -C "$root" rev-parse 'HEAD^{tree}')"
  porcelain="$(git -C "$root" status --porcelain)"
  echo "SOURCE_HEAD=$head"
  echo "SOURCE_TREE=$tree"
  echo "SOURCE_PORCELAIN<<END"
  printf '%s\n' "$porcelain"
  echo "END"
  if [[ -n "$artifact_dir" ]]; then
    mkdir -p "$artifact_dir"
    printf '%s\n' "$head" >"$artifact_dir/head.sha"
    printf '%s\n' "$tree" >"$artifact_dir/tree.sha"
    printf '%s\n' "$porcelain" >"$artifact_dir/porcelain.txt"
  fi
  if [[ -n "$porcelain" ]]; then
    echo "IDENTITY_RC=1 reason=dirty_worktree"
    return 1
  fi
  if [[ "${AGENTPULSE_CI_ENFORCE_SHA:-}" == "1" ]]; then
    if [[ -z "${EXPECTED_SHA:-}" ]]; then
      echo "IDENTITY_RC=1 reason=EXPECTED_SHA_unset"
      return 1
    fi
    if [[ "$head" != "$EXPECTED_SHA" ]]; then
      echo "IDENTITY_RC=1 reason=head_mismatch expected=$EXPECTED_SHA actual=$head"
      return 1
    fi
  elif [[ -n "${EXPECTED_SHA:-}" && "$head" != "$EXPECTED_SHA" ]]; then
    echo "IDENTITY_RC=1 reason=head_mismatch expected=$EXPECTED_SHA actual=$head"
    return 1
  fi
  echo "IDENTITY_RC=0"
  return 0
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  set -u
  root="$(cd "$(dirname "$0")/../.." && pwd)"
  verify_identity "$root" "${ARTIFACT_DIR:-}"
fi
