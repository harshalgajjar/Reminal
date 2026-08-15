# reminal installer for Windows.
#
#   irm https://raw.githubusercontent.com/harshalgajjar/Reminal/main/install.ps1 | iex
#
# or, for a specific version:
#
#   $env:REMINAL_VERSION = "v2.4.0"; irm .../install.ps1 | iex
#
# Mirrors install.sh: resolves the latest stable release, downloads the
# windows tarball, installs reminal.exe under %LOCALAPPDATA%\Programs\reminal,
# and puts that directory on the user PATH. No admin rights needed.

$ErrorActionPreference = "Stop"

$repo = "harshalgajjar/Reminal"

$arch = switch ((Get-CimInstance Win32_Processor).Architecture) {
    12      { "arm64" }   # ARM64
    default { "amd64" }
}
# Prefer the env probe modern PowerShell exposes; the CIM value above covers
# Windows PowerShell 5.1 where PROCESSOR_ARCHITECTURE can be masked by WOW64.
if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { $arch = "arm64" }

$version = $env:REMINAL_VERSION
if (-not $version) {
    # /releases/latest redirects to /releases/tag/<ver> — same tokenless trick
    # install.sh uses.
    $resp = Invoke-WebRequest -Uri "https://github.com/$repo/releases/latest" -Method Head -MaximumRedirection 0 -SkipHttpErrorCheck -ErrorAction SilentlyContinue
    $location = $resp.Headers.Location
    if ($location -is [array]) { $location = $location[0] }
    if (-not $location) { throw "could not resolve the latest release — set `$env:REMINAL_VERSION and retry" }
    $version = ($location -split "/")[-1]
}
$version = $version.TrimStart("v")

$archive = "reminal_${version}_windows_${arch}.tar.gz"
$url = "https://github.com/$repo/releases/download/v$version/$archive"

$installDir = Join-Path $env:LOCALAPPDATA "Programs\reminal"
New-Item -ItemType Directory -Force -Path $installDir | Out-Null

$tmp = Join-Path ([System.IO.Path]::GetTempPath()) "reminal-install-$PID"
New-Item -ItemType Directory -Force -Path $tmp | Out-Null
try {
    Write-Host "Downloading reminal v$version (windows/$arch)..."
    $tarball = Join-Path $tmp $archive
    Invoke-WebRequest -Uri $url -OutFile $tarball

    # tar.exe ships with Windows 10 1803+.
    tar -xzf $tarball -C $tmp
    if ($LASTEXITCODE -ne 0) { throw "failed to extract $archive" }

    $exe = Join-Path $installDir "reminal.exe"
    # A running reminal.exe can't be overwritten — but it can be renamed aside
    # (same trick the in-app updater uses).
    if (Test-Path $exe) {
        $old = "$exe.old"
        Remove-Item $old -Force -ErrorAction SilentlyContinue
        try { Move-Item $exe $old -Force -ErrorAction Stop } catch {}
    }
    Move-Item (Join-Path $tmp "reminal.exe") $exe -Force
} finally {
    Remove-Item $tmp -Recurse -Force -ErrorAction SilentlyContinue
}

# Put the install dir on the USER path (idempotent). Registry write + in-session
# update so `reminal` works in this window immediately and in every future one.
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if (($userPath -split ";") -notcontains $installDir) {
    [Environment]::SetEnvironmentVariable("Path", "$userPath;$installDir", "User")
}
if (($env:Path -split ";") -notcontains $installDir) {
    $env:Path = "$env:Path;$installDir"
}

Write-Host ""
Write-Host "reminal v$version installed to $installDir"
Write-Host ""
Write-Host "Start a session:        reminal"
Write-Host "Tab completion:         reminal completion powershell >> `$PROFILE"
Write-Host ""
Write-Host "Open a NEW terminal if 'reminal' isn't found in this one."
