<#
.SYNOPSIS
Builds Outpost on Windows with PowerShell (5.1 or newer). Same job as build.sh.

.DESCRIPTION
Needs only Go (the version in go.mod). Dependencies are vendored, so nothing is downloaded.
No sh, zip utility or osslsigncode is needed. The Windows exe is left unsigned unless you set
OUTPOST_SIGN_CMD (a command line where {exe} stands for the file to sign).

If llvm-rc and llvm-cvtres are on PATH the Windows version info is regenerated for the version
being built; otherwise the committed cmd/outpost/rsrc_windows_amd64.syso is used as it is.

.PARAMETER Target
current (default, this machine), all (the five release platforms), or one of
linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64.

.PARAMETER OutDir
Where the binaries go, in OutDir\bin. Default: ..\dist\outpost-<version>

.PARAMETER Version
Stamp this version (1.2.3) into the binaries instead of the base version in cmd/outpost/proto.go.
cmd/outpost/proto.go is restored afterwards. Same as setting OUTPOST_VERSION.

.PARAMETER Zip
Also write outpost-<version>.zip next to OutDir (uses Compress-Archive).

.EXAMPLE
.\build.ps1
.\build.ps1 all -Zip
.\build.ps1 linux-arm64 -OutDir C:\temp\out
powershell -ExecutionPolicy Bypass -File .\build.ps1
#>
param(
    [string]$Target = 'current',
    [string]$OutDir = '',
    [string]$Version = '',
    [switch]$Zip
)
$ErrorActionPreference = 'Stop'

$all = @('linux-amd64', 'linux-arm64', 'darwin-amd64', 'darwin-arm64', 'windows-amd64')
function Fail([string]$msg) { Write-Host "build.ps1: $msg" -ForegroundColor Red; exit 1 }

$root = Split-Path -Parent $MyInvocation.MyCommand.Path
Set-Location $root

# --- Go present and new enough? ---------------------------------------------------------------
$goCmd = Get-Command go -ErrorAction SilentlyContinue
$need = ''
if (Test-Path go.mod) {
    $m = [regex]::Match((Get-Content go.mod -Raw), '(?m)^go\s+(\d+\.\d+(\.\d+)?)')
    if ($m.Success) { $need = $m.Groups[1].Value }
}
if (-not $goCmd) { Fail "Go is not installed or not on PATH. Install Go $need or newer from https://go.dev/dl/ and open a new terminal." }
$goOut = (& go version) -join ' '
$m = [regex]::Match($goOut, 'go(\d+\.\d+(\.\d+)?)')
if (-not $m.Success) { Fail "cannot read the Go version from: $goOut" }
$have = $m.Groups[1].Value
if ([version]$have -lt [version]$need) { Fail "Go $have is too old: this source needs Go $need or newer (see https://go.dev/dl/)" }

# --- target -----------------------------------------------------------------------------------
$Target = $Target.Replace('/', '-')
if ($Target -eq 'host') { $Target = 'current' }
if ($Target -eq 'current') {
    $Target = ((& go env GOOS) + '-' + (& go env GOARCH))
    if ($all -notcontains $Target) { Fail "this machine ($Target) is not a supported Outpost platform; supported: $($all -join ' ')" }
}
if ($Target -eq 'all') { $targets = $all }
elseif ($all -contains $Target) { $targets = @($Target) }
else { Fail "unknown target: $Target (use all, current, or one of: $($all -join ' '))" }

if (-not $Version) { $Version = $env:OUTPOST_VERSION }
if ($Version -and $Version -notmatch '^\d+\.\d+\.\d+$') { Fail "version must look like 1.2.3, got: $Version" }

# --- save what the build touches, restore it at the end --------------------------------------
$protoPath = Join-Path $root 'cmd\outpost\proto.go'
$sysoPath = Join-Path $root 'cmd\outpost\rsrc_windows_amd64.syso'
$protoBytes = [IO.File]::ReadAllBytes($protoPath)
$sysoBytes = [IO.File]::ReadAllBytes($sysoPath)
$saveEnv = @{}
foreach ($n in 'GOOS', 'GOARCH', 'CGO_ENABLED', 'GOFLAGS', 'GOPROXY', 'GOTOOLCHAIN') { $saveEnv[$n] = [Environment]::GetEnvironmentVariable($n, 'Process') }

try {
    # Never let Go fetch a toolchain or modules: everything needed is local.
    $env:GOTOOLCHAIN = 'local'; $env:GOFLAGS = '-mod=vendor'; $env:GOPROXY = 'off'; $env:CGO_ENABLED = '0'

    if ($Version) {
        $text = [IO.File]::ReadAllText($protoPath)
        $text = [regex]::Replace($text, '(?m)^const Version = "[^"]*"', "const Version = `"$Version`"")
        [IO.File]::WriteAllText($protoPath, $text, (New-Object Text.UTF8Encoding($false)))
    }
    $vm = [regex]::Match([IO.File]::ReadAllText($protoPath), '(?m)^const Version = "([^"]*)"')
    if (-not $vm.Success) { Fail 'cannot find "const Version" in cmd/outpost/proto.go' }
    $v = $vm.Groups[1].Value
    if (-not $OutDir) { $OutDir = Join-Path (Split-Path -Parent $root) "dist\outpost-$v" }
    elseif (-not [IO.Path]::IsPathRooted($OutDir)) { $OutDir = Join-Path (Get-Location).Path $OutDir }
    $bin = Join-Path $OutDir 'bin'
    New-Item -ItemType Directory -Force -Path $bin | Out-Null

    # --- Windows version info (needs llvm-rc and llvm-cvtres) ---------------------------------
    if ($targets -contains 'windows-amd64') {
        if ((Get-Command llvm-rc -ErrorAction SilentlyContinue) -and (Get-Command llvm-cvtres -ErrorAction SilentlyContinue)) {
            $p = $v.Split('.')
            $tmp = Join-Path ([IO.Path]::GetTempPath()) ('outpost-rc-' + [Guid]::NewGuid().ToString('N'))
            New-Item -ItemType Directory -Path $tmp | Out-Null
            try {
                $rc = [IO.File]::ReadAllText((Join-Path $root 'winres\outpost.rc.in'))
                $rc = $rc.Replace('@V@', $v).Replace('@MA@', $p[0]).Replace('@MI@', $p[1]).Replace('@PA@', $p[2])
                [IO.File]::WriteAllText((Join-Path $tmp 'outpost.rc'), $rc, (New-Object Text.UTF8Encoding($false)))
                Copy-Item (Join-Path $root 'winres\outpost.manifest') $tmp
                Push-Location $tmp
                try { & llvm-rc /nologo /fo outpost.res outpost.rc } finally { Pop-Location }
                if ($LASTEXITCODE -ne 0) { Fail 'llvm-rc failed' }
                & llvm-cvtres /machine:x64 "/out:$sysoPath" (Join-Path $tmp 'outpost.res') | Out-Null
                if ($LASTEXITCODE -ne 0) { Fail 'llvm-cvtres failed' }
            } finally { Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue }
        } else {
            Write-Host "note: llvm-rc/llvm-cvtres not found; using the committed cmd/outpost/rsrc_windows_amd64.syso (its version info may differ from $v)"
        }
    }

    # --- build ---------------------------------------------------------------------------------
    $outputs = @()
    foreach ($t in $targets) {
        $os, $arch = $t.Split('-')
        $ext = ''
        # Windows keeps its symbol table: stripped, metadata-free Go exes are a common antivirus false positive.
        $ldflags = '-ldflags=-s -w -buildid='
        if ($os -eq 'windows') { $ext = '.exe'; $ldflags = '-ldflags=-buildid=' }
        $out = Join-Path $bin "outpost-$os-$arch$ext"
        Write-Host "building $os/$arch ..."
        $env:GOOS = $os; $env:GOARCH = $arch
        & go build -trimpath $ldflags -o $out ./cmd/outpost
        if ($LASTEXITCODE -ne 0) { Fail "go build failed for $t" }
        $outputs += $out
    }

    # --- optional signing (skipped unless OUTPOST_SIGN_CMD is set) -------------------------------
    $exe = Join-Path $bin 'outpost-windows-amd64.exe'
    if ($targets -contains 'windows-amd64') {
        if ($env:OUTPOST_SIGN_CMD) {
            $signCmd = $env:OUTPOST_SIGN_CMD.Replace('{exe}', "`"$exe`"")
            & cmd.exe /c $signCmd
            if ($LASTEXITCODE -ne 0) { Fail 'OUTPOST_SIGN_CMD failed' }
            Write-Host "signed $exe with OUTPOST_SIGN_CMD"
        } else {
            Write-Host 'Windows exe left unsigned (set OUTPOST_SIGN_CMD to sign it)'
        }
    }

    Write-Host "built Outpost $v into $bin"
    Write-Host 'SHA-256:'
    foreach ($f in $outputs) {
        '{0}  {1}' -f (Get-FileHash -Algorithm SHA256 -Path $f).Hash.ToLower(), (Split-Path -Leaf $f)
    }

    if ($Zip) {
        $items = @($bin)
        foreach ($d in 'README.md', 'LICENSE', 'THIRD_PARTY_NOTICES.txt') { if (Test-Path (Join-Path $root $d)) { $items += (Join-Path $root $d) } }
        $zipPath = Join-Path $OutDir "outpost-$v.zip"
        Compress-Archive -Path $items -DestinationPath $zipPath -Force
        Write-Host "wrote $zipPath"
    }
} finally {
    [IO.File]::WriteAllBytes($protoPath, $protoBytes)
    [IO.File]::WriteAllBytes($sysoPath, $sysoBytes)
    foreach ($n in $saveEnv.Keys) { [Environment]::SetEnvironmentVariable($n, $saveEnv[$n], 'Process') }
}
