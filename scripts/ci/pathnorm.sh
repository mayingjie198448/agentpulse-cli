#!/usr/bin/env bash
# 把 Go 报出的 GOROOT 和作业内安装目录归一后再比较。
# 反斜杠、盘符大小写和 Git Bash 的 /c/ 形式都先折成同一写法。
# 没有 cygpath 时只折反斜杠和盘符；/c/ 与 C:/ 的互转留在有 cygpath 的 Windows Git Bash。

canon_tool_path() {
  local p="$1"
  local converted=""
  p="${p//\\//}"
  if command -v cygpath >/dev/null 2>&1; then
    converted="$(cygpath -u "$p" 2>/dev/null || true)"
    if [[ -n "$converted" ]]; then
      p="$converted"
    fi
  fi
  if [[ "$p" =~ ^([A-Za-z]):/(.*)$ ]]; then
    local drive rest
    drive="$(printf '%s' "${BASH_REMATCH[1]}" | tr '[:lower:]' '[:upper:]')"
    rest="${BASH_REMATCH[2]}"
    p="${drive}:/${rest}"
  fi
  while [[ "$p" == */ && "$p" != "/" ]]; do
    p="${p%/}"
  done
  printf '%s' "$p"
}

goroot_is_job_local() {
  local selected="$1"
  local reported="$2"
  local selected_norm reported_norm
  selected_norm="$(canon_tool_path "$selected")"
  reported_norm="$(canon_tool_path "$reported")"
  printf '%s\n' "GOROOT_SELECTED_NORM=$selected_norm"
  printf '%s\n' "GOROOT_REPORTED_NORM=$reported_norm"
  if [[ -z "$selected_norm" || "$reported_norm" != "$selected_norm" ]]; then
    return 1
  fi
  case "$reported_norm" in
    */go-official-1.27.4/goroot) return 0 ;;
    *) return 1 ;;
  esac
}

pathnorm_self_check() {
  local failed=0
  local a b
  a="$(canon_tool_path 'C:\tmp\go-official-1.27.4\goroot')"
  b="$(canon_tool_path 'C:/tmp/go-official-1.27.4/goroot')"
  printf '%s\n' "PATHNORM_BACKSLASH=$a"
  printf '%s\n' "PATHNORM_SLASH=$b"
  if [[ "$a" != "$b" || "$a" != "C:/tmp/go-official-1.27.4/goroot" ]]; then
    echo "PATHNORM_FAIL backslash"
    failed=1
  fi
  if goroot_is_job_local 'C:\tmp\go-official-1.27.4\goroot' 'C:/tmp/go-official-1.27.4/goroot' >/tmp/pathnorm-match.out; then
    echo "PATHNORM_MATCH_RC=0"
  else
    echo "PATHNORM_FAIL expected_job_local"
    failed=1
  fi
  if goroot_is_job_local '/usr/local/go' 'C:\Windows\system32' >/tmp/pathnorm-reject.out; then
    echo "PATHNORM_FAIL system_path_accepted"
    failed=1
  else
    echo "PATHNORM_REJECT_RC=1"
  fi
  if command -v cygpath >/dev/null 2>&1; then
    echo "CYGPATH=present"
  else
    echo "CYGPATH=NOT_RUN"
  fi
  if [[ "$failed" != "0" ]]; then
    echo "PATHNORM_SELF_CHECK_RC=1"
    return 1
  fi
  echo "PATHNORM_SELF_CHECK_RC=0"
  return 0
}

if [[ "${BASH_SOURCE[0]}" == "$0" ]]; then
  if [[ "${1:-}" == "--self-check" ]]; then
    pathnorm_self_check
    exit $?
  fi
  echo "usage: pathnorm.sh --self-check" >&2
  exit 1
fi
