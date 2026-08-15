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

# Shell setup — the Windows twin of install.sh's setup_shell(): a single
# marker-guarded block in the PowerShell profile that (a) guarantees reminal is
# on PATH for every new shell and (b) loads tab completion. The PATH line looks
# redundant next to the registry write above, but it isn't: an already-running
# Windows Terminal hands new tabs its own CACHED environment, so freshly
# installed registry PATH entries don't reach them — the profile runs in every
# new shell and repairs that. Idempotent (the block is rewritten, never
# duplicated); skip with REMINAL_NO_RC=1. Written for both Windows PowerShell
# and PowerShell 7 profiles so whichever the user opens works.
if ($env:REMINAL_NO_RC -ne "1") {
    $begin = "# >>> reminal >>>"
    $end = "# <<< reminal <<<"
    $block = @"
$begin
if ((`$env:Path -split ';') -notcontains "$installDir") { `$env:Path += ";$installDir" }
if (Get-Command reminal -ErrorAction SilentlyContinue) {
    reminal completion powershell | Out-String | Invoke-Expression
}
$end
"@
    $docs = [Environment]::GetFolderPath("MyDocuments")
    foreach ($profDir in @("WindowsPowerShell", "PowerShell")) {
        $prof = Join-Path (Join-Path $docs $profDir) "profile.ps1"
        try {
            New-Item -ItemType Directory -Force -Path (Split-Path $prof) | Out-Null
            $existing = if (Test-Path $prof) { Get-Content $prof -Raw } else { "" }
            # Strip any previously-managed block so re-running rewrites exactly one.
            $existing = $existing -replace "(?ms)\r?\n?# >>> reminal >>>.*?# <<< reminal <<<\r?\n?", ""
            Set-Content -Path $prof -Value ($existing.TrimEnd() + "`r`n`r`n" + $block + "`r`n")
            Write-Host "  + reminal shell setup written to $prof"
        } catch {
            # Best-effort — never fail the install over a profile write.
        }
    }
}

Write-Host ""
Write-Host "reminal v$version installed to $installDir"
Write-Host ""
Write-Host "Start a session:        reminal"
Write-Host ""
Write-Host "Open a NEW terminal window for PATH + completion to load there."
