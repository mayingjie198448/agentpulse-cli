#!/usr/bin/env bash
# 把官方 go1.27.4 解到临时目录，并在标准输出打印本进程要用的导出语句。
# 校验值来自 https://go.dev/dl/?mode=json 的 go1.27.4 档案。不写入 /usr/local/go。

set -u

version="1.27.4"
prefix="${GO_INSTALL_PREFIX:-${RUNNER_TEMP:-${TMPDIR:-/tmp}}/go-official-${version}}"
mkdir -p "$prefix"

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
archive="$prefix/$name"
root="$prefix/goroot"
if [[ ! -x "$root/bin/go" && ! -x "$root/bin/go.exe" ]]; then
  url="https://go.dev/dl/$name"
  echo "GO_DOWNLOAD url=$url" >&2
  curl -fsSL "$url" -o "$archive"
  if command -v sha256sum >/dev/null 2>&1; then
    got="$(sha256sum "$archive" | awk '{print $1}')"
  else
    got="$(shasum -a 256 "$archive" | awk '{print $1}')"
  fi
  if [[ "$got" != "$want" ]]; then
    echo "GO_CHECKSUM_MISMATCH got=$got want=$want" >&2
    exit 1
  fi
  rm -rf "$prefix/unpack"
  mkdir -p "$prefix/unpack"
  if [[ "$ext" == "zip" ]]; then
    if command -v unzip >/dev/null 2>&1; then
      unzip -q "$archive" -d "$prefix/unpack"
    else
      tar -xf "$archive" -C "$prefix/unpack"
    fi
  else
    tar -xzf "$archive" -C "$prefix/unpack"
  fi
  rm -rf "$root"
  mv "$prefix/unpack/go" "$root"
fi

printf 'export GOROOT=%q\n' "$root"
printf 'export PATH=%q\n' "$root/bin:$PATH"
printf 'export GOTOOLCHAIN=%q\n' "local"
printf 'GO_INSTALL_ISOLATED=yes\n' >&2
printf 'GOROOT_SELECTED=%s\n' "$root" >&2
