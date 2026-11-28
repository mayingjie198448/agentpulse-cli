#!/usr/bin/env bash
# 把官方 go1.27.4 解到本作业的临时目录，并在标准输出打印本进程要用的导出语句。
# 官方归档放在 runner 跨作业仍保留的目录里；GOROOT 每次从校验过的归档重新解压。
# 下载先试 https://golang.google.cn/dl/ （Google 官方中国镜像），失败再试 https://go.dev/dl/ 。
# 校验值来自 https://go.dev/dl/?mode=json 的 go1.27.4 档案，两个源共用同一组 sha256。不写入 /usr/local/go。
# 下载或校验失败时直接以非零退出，不回退到系统已安装的 Go。

set -u

version="1.27.4"
partial=""

# 失败退出时清掉未完成的下载，避免半截文件被下次当成缓存。
# macOS 的 bash 3.2 会用 EXIT trap 的返回码替换脚本退出码，所以要记下来再退出。
cleanup_partial() {
  status=$?
  if [[ -n "$partial" && -f "$partial" ]]; then
    rm -f "$partial"
  fi
  exit "$status"
}
trap cleanup_partial EXIT

# 把 runner 给的 Windows 路径转成当前 shell 能 mkdir 的路径。
# 已是 POSIX 路径时保持原样。没有 cygpath（macOS /bin/bash 3.2）时只处理盘符和反斜杠。
to_posix_path() {
  local p="$1"
  local converted=""
  local drive=""
  local rest=""
  if [[ -z "$p" ]]; then
    printf '%s' ""
    return 0
  fi
  if command -v cygpath >/dev/null 2>&1; then
    if converted="$(cygpath -u "$p" 2>/dev/null)"; then
      if [[ -n "$converted" ]]; then
        printf '%s' "$converted"
        return 0
      fi
    fi
  fi
  p="${p//\\//}"
  if [[ "$p" =~ ^([A-Za-z]):/(.*)$ ]]; then
    drive="$(printf '%s' "${BASH_REMATCH[1]}" | tr '[:upper:]' '[:lower:]')"
    rest="${BASH_REMATCH[2]}"
    printf '/%s/%s' "$drive" "$rest"
    return 0
  fi
  if [[ "$p" =~ ^([A-Za-z]):$ ]]; then
    drive="$(printf '%s' "${BASH_REMATCH[1]}" | tr '[:upper:]' '[:lower:]')"
    printf '/%s' "$drive"
    return 0
  fi
  printf '%s' "$p"
}

# 去掉结尾的斜杠，但不要把根目录削成空字符串。
strip_trailing_slash() {
  local p="$1"
  while [[ "$p" == */ && "$p" != "/" ]]; do
    p="${p%/}"
  done
  printf '%s' "$p"
}

uname_s="$(uname -s)"
uname_m="$(uname -m)"
os=""
arch=""
ext=""
want=""
case "$uname_s" in
  Linux)
    os="linux"
    arch="amd64"
    ext="tar.gz"
    want="7226736c2339719cda28fbf3a54d71aa63bcefe376e98cef00034268dab24840"
    ;;
  Darwin)
    os="darwin"
    case "$uname_m" in
      arm64) arch="arm64"; want="91a44a2b8a716600ad94a4f143e156116620db8828086a6e3602291be9bd0bb4" ;;
      x86_64) arch="amd64"; want="2b799377704ef12db39bd254995d5c348c9cc8d86562ca817724390e56c38f43" ;;
      *) echo "unsupported darwin arch $uname_m" >&2; exit 1 ;;
    esac
    ext="tar.gz"
    ;;
  MINGW*|MSYS*|CYGWIN*)
    os="windows"
    arch="amd64"
    ext="zip"
    want="178ab98950ac69ab25069307dc6ade79f3816e414cce95b4da166a115cde1e7e"
    ;;
  *)
    echo "unsupported os $uname_s" >&2
    exit 1
    ;;
esac

name="go${version}.${os}-${arch}.${ext}"

# 归档目录：显式前缀优先，否则用跨作业的 RUNNER_TOOL_CACHE，再没有才退回作业临时目录。
install_prefix="$(to_posix_path "${GO_INSTALL_PREFIX:-}")"
tool_cache="$(to_posix_path "${RUNNER_TOOL_CACHE:-}")"
job_temp="$(to_posix_path "${RUNNER_TEMP:-}")"
if [[ -z "$job_temp" ]]; then
  job_temp="$(to_posix_path "${TMPDIR:-/tmp}")"
fi
install_prefix="$(strip_trailing_slash "$install_prefix")"
tool_cache="$(strip_trailing_slash "$tool_cache")"
job_temp="$(strip_trailing_slash "$job_temp")"

cache_backend=""
if [[ -n "$install_prefix" ]]; then
  cache_backend="GO_INSTALL_PREFIX"
  cache_dir="$install_prefix"
elif [[ -n "$tool_cache" ]]; then
  cache_backend="RUNNER_TOOL_CACHE"
  cache_dir="${tool_cache}/agentpulse-go/${version}"
else
  cache_backend="JOB_TEMP"
  cache_dir="${job_temp}/go-official-${version}"
fi

# 解压目录必须落在作业临时目录，且以 go-official-1.27.4/goroot 结尾，供 pathnorm 判定作业内隔离。
job_dir="${job_temp}/go-official-${version}"
archive="${cache_dir}/${name}"
root="${job_dir}/goroot"

if [[ -z "$cache_dir" || -z "$job_dir" || -z "$archive" || -z "$want" ]]; then
  echo "GO_PATH_EMPTY" >&2
  exit 1
fi
case "$job_dir" in
  */go-official-1.27.4) ;;
  *)
    echo "GO_JOB_DIR_UNEXPECTED path=$job_dir" >&2
    exit 1
    ;;
esac

# 校验指定文件。source_url 只用于日志。不符返回 1，由调用方删除。
# 不信任调用方传来的路径以外的旧树，也不因为换了下载源就换校验值。
verify_archive() {
  local path="$1"
  local source_url="${2:-}"
  local got=""
  local tool=""
  if [[ ! -f "$path" ]]; then
    echo "GO_ARCHIVE_MISSING path=$path" >&2
    return 1
  fi
  if command -v sha256sum >/dev/null 2>&1; then
    tool="sha256sum"
    got="$(sha256sum "$path" | awk '{print $1}')"
  elif command -v shasum >/dev/null 2>&1; then
    tool="shasum"
    got="$(shasum -a 256 "$path" | awk '{print $1}')"
  else
    echo "GO_CHECKSUM_TOOL_MISSING" >&2
    return 1
  fi
  if [[ -n "$source_url" ]]; then
    echo "GO_SOURCE_URL=$source_url" >&2
  fi
  echo "GO_CHECKSUM_TOOL=$tool" >&2
  echo "GO_CHECKSUM_EXPECTED=$want" >&2
  echo "GO_CHECKSUM_ACTUAL=$got" >&2
  if [[ -z "$got" || "$got" != "$want" ]]; then
    echo "GO_CHECKSUM_MISMATCH=yes path=$path" >&2
    return 1
  fi
  echo "GO_CHECKSUM_OK=yes path=$path" >&2
  return 0
}

# 从一个源下载到临时文件。校验通过并改名到缓存后返回 0。
# partial 必须是全局变量，EXIT trap 才能在中途失败时删掉半截文件。
download_from() {
  local source_url="$1"
  local effective=""
  partial="${cache_dir}/.${name}.partial.$$"
  rm -f "$partial"
  echo "GO_DOWNLOAD url=$source_url" >&2
  echo "GO_DOWNLOAD_PARTIAL=$partial" >&2
  # -f 在 -fsSL 中。每个源单独重试；--max-time 截断单次传输，--retry-max-time 截断这一源的重试窗口。
  if ! effective="$(curl -fsSL \
    --retry 3 \
    --retry-delay 5 \
    --retry-all-errors \
    --connect-timeout 30 \
    --max-time 600 \
    --retry-max-time 600 \
    --output "$partial" \
    -w '%{url_effective}' \
    "$source_url")"; then
    echo "GO_DOWNLOAD_FAILED url=$source_url" >&2
    rm -f "$partial"
    partial=""
    return 1
  fi
  # Windows curl 的 -w 可能带 CR。命令替换已经去掉结尾换行。
  effective="${effective//$'\r'/}"
  if [[ -z "$effective" ]]; then
    effective="$source_url"
  fi
  echo "GO_DOWNLOAD_EFFECTIVE_URL=$effective" >&2
  if ! verify_archive "$partial" "$effective"; then
    echo "GO_DOWNLOAD_CHECKSUM_FAILED url=$effective path=$partial" >&2
    rm -f "$partial"
    partial=""
    return 1
  fi
  if ! mv -f "$partial" "$archive"; then
    echo "GO_CACHE_RENAME_FAILED from=$partial to=$archive" >&2
    rm -f "$partial"
    partial=""
    return 1
  fi
  partial=""
  # 使用前再校验已经落在缓存位置的归档，避免改名后的文件和临时文件不一致。
  if ! verify_archive "$archive" "$effective"; then
    echo "GO_CACHE_POST_RENAME_MISMATCH url=$effective path=$archive" >&2
    rm -f "$archive"
    return 1
  fi
  echo "GO_DOWNLOAD_URL=$effective" >&2
  return 0
}

if ! mkdir -p "$cache_dir" "$job_dir"; then
  echo "GO_DIR_CREATE_FAILED cache=$cache_dir job=$job_dir" >&2
  exit 1
fi

echo "GO_CACHE_BACKEND=$cache_backend" >&2
echo "GO_CACHE_PATH=$archive" >&2
echo "GO_EXTRACT_DIR=$job_dir" >&2

cache_ok=0
if [[ -f "$archive" ]]; then
  echo "GO_CACHE=hit path=$archive" >&2
  if verify_archive "$archive"; then
    echo "GO_CACHE_VERIFIED=yes" >&2
    echo "GO_DOWNLOAD_URL=none reason=cache_hit" >&2
    cache_ok=1
  else
    echo "GO_CACHE_REJECTED=yes" >&2
    rm -f "$archive"
  fi
else
  echo "GO_CACHE=miss path=$archive" >&2
fi

if [[ "$cache_ok" != "1" ]]; then
  downloaded=0
  # 大陆网络先走 Google 官方中国镜像，失败再走 go.dev。校验值不随源改变。
  for source_url in \
    "https://golang.google.cn/dl/${name}" \
    "https://go.dev/dl/${name}"
  do
    if download_from "$source_url"; then
      downloaded=1
      break
    fi
    echo "GO_DOWNLOAD_SOURCE_FAILED url=$source_url" >&2
  done
  if [[ "$downloaded" != "1" ]]; then
    echo "GO_DOWNLOAD_ALL_SOURCES_FAILED" >&2
    exit 1
  fi
fi

# 每次作业都从校验通过的归档重新解压到干净目录，不执行上次留下的解压树。
rm -rf "${job_dir}/unpack" "${job_dir}/goroot"
if ! mkdir -p "${job_dir}/unpack"; then
  echo "GO_UNPACK_DIR_CREATE_FAILED path=${job_dir}/unpack" >&2
  exit 1
fi
unpack_rc=0
if [[ "$ext" == "zip" ]]; then
  if command -v unzip >/dev/null 2>&1; then
    unzip -q "$archive" -d "${job_dir}/unpack" || unpack_rc=$?
  else
    tar -xf "$archive" -C "${job_dir}/unpack" || unpack_rc=$?
  fi
else
  tar -xzf "$archive" -C "${job_dir}/unpack" || unpack_rc=$?
fi
if [[ "$unpack_rc" != "0" ]]; then
  echo "GO_UNPACK_FAILED rc=$unpack_rc" >&2
  rm -rf "${job_dir}/unpack"
  exit 1
fi
if [[ ! -d "${job_dir}/unpack/go" ]]; then
  echo "GO_UNPACK_MISSING_GO_DIR" >&2
  rm -rf "${job_dir}/unpack"
  exit 1
fi
if ! mv "${job_dir}/unpack/go" "$root"; then
  echo "GO_UNPACK_MOVE_FAILED" >&2
  rm -rf "${job_dir}/unpack"
  exit 1
fi
rm -rf "${job_dir}/unpack"

if [[ ! -x "$root/bin/go" && ! -x "$root/bin/go.exe" ]]; then
  echo "GO_BINARY_NOT_EXECUTABLE path=$root/bin" >&2
  exit 1
fi

# 标准输出只放导出语句，日志都在标准错误，调用方会 eval 标准输出。
printf 'export GOROOT=%q\n' "$root"
printf 'export AGENTPULSE_GOROOT_SELECTED=%q\n' "$root"
printf 'export PATH=%q\n' "$root/bin:$PATH"
printf 'export GOTOOLCHAIN=%q\n' "local"
printf 'GO_INSTALL_ISOLATED=yes\n' >&2
printf 'GOROOT_SELECTED=%s\n' "$root" >&2
