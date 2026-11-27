#!/usr/bin/env bash
# 把 Go 报出的 GOROOT 和作业内安装目录归一后再比较。
# 统一将 Windows 盘符路径（如 C:\...、C:/...、c:\...、/c/...、/cygdrive/c/...）归一为 /c/... 格式。
# 无论环境中是否存在 cygpath，比较规则保持一致。
# 保持 GOROOT 必须属于本作业隔离目录的约束。

canon_tool_path() {
  local p="$1"
  local converted=""
  p="${p//\\//}"
  if [[ "${PATHNORM_NO_CYGPATH:-0}" != "1" ]] && command -v cygpath >/dev/null 2>&1; then
    if converted="$(cygpath -u "$p" 2>/dev/null)"; then
      if [[ -n "$converted" ]]; then
        p="$converted"
      fi
    fi
  fi
  if [[ "$p" =~ ^/cygdrive/([A-Za-z])/(.*)$ ]]; then
    local drive rest
    drive="$(printf '%s' "${BASH_REMATCH[1]}" | tr '[:upper:]' '[:lower:]')"
    rest="${BASH_REMATCH[2]}"
    p="/${drive}/${rest}"
  elif [[ "$p" =~ ^/cygdrive/([A-Za-z])$ ]]; then
    local drive
    drive="$(printf '%s' "${BASH_REMATCH[1]}" | tr '[:upper:]' '[:lower:]')"
    p="/${drive}"
  elif [[ "$p" =~ ^([A-Za-z]):/(.*)$ ]]; then
    local drive rest
    drive="$(printf '%s' "${BASH_REMATCH[1]}" | tr '[:upper:]' '[:lower:]')"
    rest="${BASH_REMATCH[2]}"
    p="/${drive}/${rest}"
  elif [[ "$p" =~ ^([A-Za-z]):$ ]]; then
    local drive
    drive="$(printf '%s' "${BASH_REMATCH[1]}" | tr '[:upper:]' '[:lower:]')"
    p="/${drive}"
  elif [[ "$p" =~ ^/([A-Za-z])/(.*)$ ]]; then
    local drive rest
    drive="$(printf '%s' "${BASH_REMATCH[1]}" | tr '[:upper:]' '[:lower:]')"
    rest="${BASH_REMATCH[2]}"
    p="/${drive}/${rest}"
  elif [[ "$p" =~ ^/([A-Za-z])$ ]]; then
    local drive
    drive="$(printf '%s' "${BASH_REMATCH[1]}" | tr '[:upper:]' '[:lower:]')"
    p="/${drive}"
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
  local want="/c/tmp/go-official-1.27.4/goroot"
  local a b c_lower d_ps

  # 1. 验证反斜杠与正斜杠归一化
  a="$(canon_tool_path 'C:\tmp\go-official-1.27.4\goroot')"
  b="$(canon_tool_path 'C:/tmp/go-official-1.27.4/goroot')"
  printf '%s\n' "PATHNORM_BACKSLASH=$a"
  printf '%s\n' "PATHNORM_SLASH=$b"
  if [[ "$a" != "$want" || "$b" != "$want" ]]; then
    echo "PATHNORM_FAIL slash_backslash_norm"
    failed=1
  fi

  # 2. 验证盘符大小写归一化
  c_lower="$(canon_tool_path 'c:\tmp\go-official-1.27.4\goroot')"
  printf '%s\n' "PATHNORM_DRIVE_CASE=$c_lower"
  if [[ "$c_lower" != "$want" ]]; then
    echo "PATHNORM_FAIL drive_case_norm"
    failed=1
  fi

  # 3. 验证 PowerShell (Convert-ToGitBashPath) 传递给 Git Bash 的路径形式
  d_ps="$(canon_tool_path '/c/tmp/go-official-1.27.4/goroot')"
  printf '%s\n' "PATHNORM_PS_PASSED=$d_ps"
  if [[ "$d_ps" != "$want" ]]; then
    echo "PATHNORM_FAIL ps_passed_path_norm"
    failed=1
  fi

  # 4. 验证作业内隔离路径判定：PowerShell /c/... 与 Windows go env C:\... 相互匹配
  if goroot_is_job_local '/c/tmp/go-official-1.27.4/goroot' 'C:\tmp\go-official-1.27.4\goroot' >/dev/null 2>&1; then
    echo "PATHNORM_MATCH_RC=0"
  else
    echo "PATHNORM_FAIL expected_job_local_match"
    failed=1
  fi

  # 5. 验证非作业内或系统路径必须被拒绝
  if goroot_is_job_local '/usr/local/go' 'C:\Windows\system32' >/dev/null 2>&1; then
    echo "PATHNORM_FAIL system_path_accepted"
    failed=1
  else
    echo "PATHNORM_REJECT_RC=1"
  fi
  if goroot_is_job_local 'C:\Program Files\Go' 'C:\Program Files\Go' >/dev/null 2>&1; then
    echo "PATHNORM_FAIL non_job_local_accepted"
    failed=1
  else
    echo "PATHNORM_REJECT_NON_JOB_LOCAL_RC=1"
  fi

  # 6. 验证无 cygpath 时的归一化行为（强制纯 bash 规则）
  local no_cyg_bs no_cyg_ps no_cyg_lower
  no_cyg_bs="$(PATHNORM_NO_CYGPATH=1 canon_tool_path 'C:\tmp\go-official-1.27.4\goroot')"
  no_cyg_ps="$(PATHNORM_NO_CYGPATH=1 canon_tool_path '/c/tmp/go-official-1.27.4/goroot')"
  no_cyg_lower="$(PATHNORM_NO_CYGPATH=1 canon_tool_path 'c:\tmp\go-official-1.27.4\goroot')"
  if [[ "$no_cyg_bs" != "$want" || "$no_cyg_ps" != "$want" || "$no_cyg_lower" != "$want" ]]; then
    echo "PATHNORM_FAIL pure_bash_no_cygpath"
    failed=1
  else
    echo "PATHNORM_NO_CYGPATH_OK=yes"
  fi

  # 7. 验证有 cygpath 时的环境状态及归一化
  if command -v cygpath >/dev/null 2>&1; then
    echo "CYGPATH=present"
    local cyg_bs cyg_ps
    cyg_bs="$(canon_tool_path 'C:\tmp\go-official-1.27.4\goroot')"
    cyg_ps="$(canon_tool_path '/c/tmp/go-official-1.27.4/goroot')"
    if [[ "$cyg_bs" != "$want" || "$cyg_ps" != "$want" ]]; then
      echo "PATHNORM_FAIL cygpath_present_norm"
      failed=1
    else
      echo "PATHNORM_CYGPATH_PRESENT_OK=yes"
    fi
  else
    echo "CYGPATH=NOT_RUN"
    # 在非 Windows 环境下用模拟 cygpath 验证有 cygpath 时的调用分支
    local sim_tmp
    sim_tmp="$(mktemp -d "${TMPDIR:-/tmp}/pathnorm-sim.XXXXXX")"
    cat >"$sim_tmp/cygpath" <<'MOCK_CYGPATH'
#!/bin/sh
p="$1"
if [ "$p" = "-u" ]; then p="$2"; fi
p="$(printf '%s' "$p" | tr '\\' '/')"
case "$p" in
  [A-Za-z]:/*)
    d="$(printf '%s' "$p" | cut -c1 | tr '[:upper:]' '[:lower:]')"
    rest="$(printf '%s' "$p" | cut -c4-)"
    printf '/%s/%s\n' "$d" "$rest"
    ;;
  *)
    printf '%s\n' "$p"
    ;;
esac
MOCK_CYGPATH
    chmod +x "$sim_tmp/cygpath"
    local sim_bs sim_ps
    sim_bs="$(PATH="$sim_tmp:$PATH" canon_tool_path 'C:\tmp\go-official-1.27.4\goroot')"
    sim_ps="$(PATH="$sim_tmp:$PATH" canon_tool_path '/c/tmp/go-official-1.27.4/goroot')"
    rm -rf "$sim_tmp"
    if [[ "$sim_bs" != "$want" || "$sim_ps" != "$want" ]]; then
      echo "PATHNORM_FAIL simulated_cygpath"
      failed=1
    else
      echo "PATHNORM_SIMULATED_CYGPATH_OK=yes"
    fi
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
