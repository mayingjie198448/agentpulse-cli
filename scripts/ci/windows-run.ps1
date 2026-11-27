# Windows 作业入口。由已经启动的 PowerShell 步骤调用。
# 不使用 ExecutionPolicy Bypass，不改 ACL，不调用 system32 的 WSL bash。
$ErrorActionPreference = 'Stop'
if (-not $env:ARTIFACT_DIR) { throw 'ARTIFACT_DIR is required' }
if (-not $env:WIN_SCRIPT) { throw 'WIN_SCRIPT is required' }
New-Item -ItemType Directory -Force -Path $env:ARTIFACT_DIR | Out-Null
$log = Join-Path $env:ARTIFACT_DIR 'windows-shell.txt'
function Write-Diag([string]$message) {
  Add-Content -LiteralPath $log -Value $message
  Write-Output $message
}
Write-Diag 'WIN_SHELL_DIAG=start'
$wsl = Join-Path $env:SystemRoot 'system32\bash.exe'
Write-Diag "REJECTED_WSL_BASH=$wsl reason=WSL_E_LOCAL_SYSTEM_NOT_SUPPORTED"
$candidates = @(
  (Join-Path $env:ProgramFiles 'Git\bin\bash.exe')
)
if (${env:ProgramFiles(x86)}) {
  $candidates += (Join-Path ${env:ProgramFiles(x86)} 'Git\bin\bash.exe')
}
$bash = $null
foreach ($candidate in $candidates) {
  if ($candidate -and (Test-Path -LiteralPath $candidate)) {
    $bash = $candidate
    break
  }
}
if (-not $bash) {
  Write-Diag 'GIT_BASH=missing'
  exit 1
}
Write-Diag "GIT_BASH=$bash"
function Convert-ToGitBashPath([string]$path) {
  if ([string]::IsNullOrEmpty($path)) { return $path }
  $slash = $path.Replace('\', '/')
  if ($slash -match '^([A-Za-z]):/(.*)$') {
    return '/' + $Matches[1].ToLowerInvariant() + '/' + $Matches[2]
  }
  return $slash
}
foreach ($name in @('ARTIFACT_DIR', 'TARGET_ROOT', 'RUNNER_TEMP', 'TMPDIR', 'GO_INSTALL_PREFIX')) {
  $current = [Environment]::GetEnvironmentVariable($name)
  if (-not [string]::IsNullOrEmpty($current)) {
    $converted = Convert-ToGitBashPath $current
    Write-Diag "PATH_ENV $name ps=$current bash=$converted"
    [Environment]::SetEnvironmentVariable($name, $converted)
  }
}
Write-Diag "WIN_SCRIPT=$env:WIN_SCRIPT"
& $bash --noprofile --norc -eo pipefail $env:WIN_SCRIPT
$code = $LASTEXITCODE
Write-Diag "WIN_SCRIPT_RC=$code"
exit $code
