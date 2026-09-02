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
    # /releases/latest redirects to /releases/tag/<ver> -- same tokenless trick
    # install.sh uses. Invoke-WebRequest can't observe an un-followed 302
    # portably across PowerShell 5.1 and 7 (each treats it as a different
    # error shape), so drop to .NET WebRequest, where a 3xx with
    # AllowAutoRedirect=$false is just a normal response. Works identically in
    # both generations; the SecurityProtocol bump covers 5.1 boxes still
    # defaulting below TLS 1.2.
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor 3072
    $req = [System.Net.WebRequest]::Create("https://github.com/$repo/releases/latest")
    $req.Method = "HEAD"
    $req.AllowAutoRedirect = $false
    $location = $null
    try {
        $resp = $req.GetResponse()
        $location = $resp.Headers["Location"]
        $resp.Close()
    } catch {}
    if (-not $location) { throw "could not resolve the latest release -- set `$env:REMINAL_VERSION and retry" }
    $version = ("$location" -split "/")[-1]
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
    # -UseBasicParsing: required on stock Windows PowerShell 5.1 boxes where the
    # IE parsing engine was never initialized; harmless (deprecated no-op) on 7+.
    Invoke-WebRequest -Uri $url -OutFile $tarball -UseBasicParsing

    # tar.exe ships with Windows 10 1803+.
    tar -xzf $tarball -C $tmp
    if ($LASTEXITCODE -ne 0) { throw "failed to extract $archive" }

    $exe = Join-Path $installDir "reminal.exe"
    # A running reminal.exe can't be overwritten -- but it can be renamed aside
    # (same trick the in-app updater uses). The name has to be UNIQUE per
    # install: the binary we displace is usually still executing -- the
    # always-on daemon, any live session's agent, every pty holder -- and
    # Windows will neither delete nor overwrite a file whose image is mapped.
    # Reusing one fixed ".old" name worked exactly once per machine; the next
    # upgrade found it locked and died with "Access is denied".
    if (Test-Path $exe) {
        $old = Join-Path $installDir (".reminal.old-{0}" -f [DateTimeOffset]::UtcNow.ToUnixTimeMilliseconds())
        try { Move-Item $exe $old -Force -ErrorAction Stop }
        catch { throw "could not move the running $exe aside: $_" }
    }
    Move-Item (Join-Path $tmp "reminal.exe") $exe -Force
    # Retire what earlier installs displaced. Anything still running refuses,
    # and gets swept by the install after it stops -- that is why the names
    # are unique rather than reused.
    Get-ChildItem -Path $installDir -Filter ".reminal.old-*" -Force -ErrorAction SilentlyContinue |
        ForEach-Object { Remove-Item $_.FullName -Force -ErrorAction SilentlyContinue }
    Remove-Item (Join-Path $installDir "reminal.exe.old") -Force -ErrorAction SilentlyContinue
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

# Shell setup -- the Windows twin of install.sh's setup_shell(): a single
# marker-guarded block in the PowerShell profile that (a) guarantees reminal is
# on PATH for every new shell and (b) loads tab completion. The PATH line looks
# redundant next to the registry write above, but it isn't: an already-running
# Windows Terminal hands new tabs its own CACHED environment, so freshly
# installed registry PATH entries don't reach them -- the profile runs in every
# new shell and repairs that. Idempotent (the block is rewritten, never
# duplicated); skip with REMINAL_NO_RC=1. Written for both Windows PowerShell
# and PowerShell 7 profiles so whichever the user opens works.
if ($env:REMINAL_NO_RC -ne "1") {
    # Stock Windows clients ship with execution policy "Restricted", which
    # blocks ALL script files -- including the profile blocks below, which
    # would then print a red PSSecurityException in every new shell instead
    # of loading. Lift the policy to RemoteSigned (locally created scripts
    # run; downloaded ones still need unblocking) for the CURRENT USER only --
    # no admin, and both engines get it since each keeps its own setting. If
    # policy is enforced by Group Policy this fails; then we must NOT write
    # profiles at all (they'd error on every launch), so fall back to a hint.
    $profilesOk = $true
    if ((Get-ExecutionPolicy) -in @("Restricted", "AllSigned")) {
        try {
            Set-ExecutionPolicy -Scope CurrentUser -ExecutionPolicy RemoteSigned -Force -ErrorAction Stop
            Write-Host "  + profile scripts enabled (execution policy: CurrentUser -> RemoteSigned)"
        } catch {
            $profilesOk = $false
        }
        foreach ($engine in @("powershell", "pwsh")) {
            if (Get-Command $engine -ErrorAction SilentlyContinue) {
                & $engine -NoProfile -Command "try { Set-ExecutionPolicy -Scope CurrentUser -ExecutionPolicy RemoteSigned -Force } catch {}" 2>$null | Out-Null
            }
        }
    }
    if (-not $profilesOk) {
        Write-Host "  ! execution policy is locked down (likely Group Policy) -- skipping profile setup."
        Write-Host "    PATH is set for new processes; for completion, run: reminal completion powershell | Out-String | Invoke-Expression"
        $env:REMINAL_NO_RC = "1"
    }
}
if ($env:REMINAL_NO_RC -ne "1") {
    $begin = "# >>> reminal >>>"
    $end = "# <<< reminal <<<"
    $block = @"
$begin
if ((`$env:Path -split ';') -notcontains "$installDir") { `$env:Path += ";$installDir" }
if (Get-Command reminal -ErrorAction SilentlyContinue) {
    reminal completion powershell | Out-String | Invoke-Expression
}
# Keep the PROCESS working directory in sync with Set-Location. PowerShell
# deliberately doesn't chdir on cd (locations are per-runspace), which leaves
# the directory other tools read (reminal's Dir column, anything inspecting
# the process) frozen at launch. Wrapping the prompt is the only mechanism
# that works on BOTH Windows PowerShell 5.1 and pwsh (LocationChangedAction
# is 6+); the existing prompt is preserved by delegation, and the wrap-once
# guard keeps installer re-runs from stacking wrappers.
try {
    if (`$null -eq `$global:__reminalPromptBase) {
        `$global:__reminalPromptBase = `$function:prompt
        function global:prompt {
            try {
                if (`$pwd.Provider.Name -eq 'FileSystem') {
                    [Environment]::CurrentDirectory = `$pwd.ProviderPath
                    # OSC 9;9 -- announce the cwd to the hosting terminal
                    # (Windows Terminal convention; reminal picks it up as an
                    # instant, event-driven Dir update). Invisible on screen.
                    [Console]::Write([char]27 + ']9;9;' + `$pwd.ProviderPath + [char]7)
                }
            } catch {}
            & `$global:__reminalPromptBase
        }
    }
} catch {}
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
            # Best-effort -- never fail the install over a profile write.
        }
    }
}

# Windows citizenship: an Apps & Features entry backed by a local
# uninstall.ps1 that reverses everything this installer did. HKCU only --
# no admin, uninstall scoped to this user.
$uninst = Join-Path $installDir "uninstall.ps1"
@'
# reminal uninstaller (written by install.ps1)
$ErrorActionPreference = "SilentlyContinue"
$installDir = Split-Path -Parent $MyInvocation.MyCommand.Path
Get-Process reminal | Stop-Process -Force
Start-Sleep 1
# autostart + Apps & Features entries
Remove-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Run" -Name "reminal-daemon"
Remove-ItemProperty -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Explorer\StartupApproved\Run" -Name "reminal-daemon"
Remove-Item -Path "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\reminal" -Recurse
# user PATH entry
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath) {
    $newPath = ($userPath -split ";" | Where-Object { $_ -and $_ -ne $installDir }) -join ";"
    [Environment]::SetEnvironmentVariable("Path", $newPath, "User")
}
# profile blocks
$docs = [Environment]::GetFolderPath("MyDocuments")
foreach ($profDir in @("WindowsPowerShell", "PowerShell")) {
    $prof = Join-Path (Join-Path $docs $profDir) "profile.ps1"
    if (Test-Path $prof) {
        $existing = Get-Content $prof -Raw
        $stripped = $existing -replace "(?ms)\r?\n?# >>> reminal >>>.*?# <<< reminal <<<\r?\n?", ""
        Set-Content -Path $prof -Value $stripped.TrimEnd()
    }
}
Write-Host "reminal removed. Session data in $env:USERPROFILE\.reminal was kept; delete it to remove keys and history."
# The install dir contains THIS script and possibly a running console's exe --
# delete it from a detached shell after we exit.
Start-Process cmd -ArgumentList "/c", "timeout /t 2 /nobreak >nul & rmdir /s /q `"$installDir`"" -WindowStyle Hidden
'@ | Set-Content -Path $uninst

$uk = "HKCU:\Software\Microsoft\Windows\CurrentVersion\Uninstall\reminal"
New-Item -Path $uk -Force | Out-Null
Set-ItemProperty -Path $uk -Name "DisplayName" -Value "reminal"
Set-ItemProperty -Path $uk -Name "DisplayVersion" -Value $version
Set-ItemProperty -Path $uk -Name "Publisher" -Value "reminal"
Set-ItemProperty -Path $uk -Name "DisplayIcon" -Value (Join-Path $installDir "reminal.exe")
Set-ItemProperty -Path $uk -Name "InstallLocation" -Value $installDir
Set-ItemProperty -Path $uk -Name "UninstallString" -Value "powershell.exe -NoProfile -ExecutionPolicy Bypass -File `"$uninst`""
Set-ItemProperty -Path $uk -Name "NoModify" -Value 1 -Type DWord
Set-ItemProperty -Path $uk -Name "NoRepair" -Value 1 -Type DWord

Write-Host ""
Write-Host "reminal v$version installed to $installDir"
Write-Host ""
Write-Host "Start a session:        reminal"
Write-Host ""
Write-Host "Open a NEW terminal window for PATH + completion to load there."
