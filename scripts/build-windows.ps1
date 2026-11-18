$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest

# Build OCcodeBridge as a CPA native plugin on Windows (dist\occodebridge.dll).
#
# A CPA native plugin must be linked as a shared library (-buildmode=c-shared),
# so it needs a C toolchain. This script never downloads or installs one: it
# looks for the local winlibs MinGW-w64 gcc, then for a gcc on PATH, and fails
# with the real blocker when neither exists.
#
#   pwsh -NoProfile -File scripts/build-windows.ps1            # build only
#   pwsh -NoProfile -File scripts/build-windows.ps1 -Zip       # build and package
#
# -Zip writes dist\occodebridge_<version>_windows_amd64.zip with occodebridge.dll at
# its root, the same name the release workflow publishes.

$repoRoot = Split-Path -Parent $PSScriptRoot
$outputPath = Join-Path $repoRoot 'dist\occodebridge.dll'
$packageZip = $args -contains '-Zip'
$compilerArgument = ''
for ($index = 0; $index -lt $args.Count; $index++) {
    if ($args[$index] -eq '-Compiler' -and $index + 1 -lt $args.Count) {
        $compilerArgument = [string]$args[$index + 1]
    }
}

function Get-RegistryVersion {
    $registryPath = Join-Path $repoRoot 'marketplace\registry.json'
    if (-not (Test-Path -LiteralPath $registryPath)) {
        throw "marketplace/registry.json is missing; the release workflow reads the plugin version from it."
    }
    $registry = Get-Content -LiteralPath $registryPath -Raw -Encoding utf8 | ConvertFrom-Json
    $version = [string]$registry.plugins[0].version
    if ([string]::IsNullOrWhiteSpace($version)) {
        throw 'marketplace/registry.json has no plugins[0].version.'
    }
    return $version
}

# Lookup order: -Compiler, $env:CC, gcc on PATH, the local .tools winlibs install
# next to this repository. The first hit that actually runs gcc wins.
function Resolve-Compiler([string]$Explicit) {
    $candidates = New-Object System.Collections.Generic.List[string]
    if (-not [string]::IsNullOrWhiteSpace($Explicit)) { $candidates.Add($Explicit) }
    if (-not [string]::IsNullOrWhiteSpace($env:CC)) { $candidates.Add([string]$env:CC) }
    $onPath = Get-Command gcc -ErrorAction SilentlyContinue
    if ($onPath) { $candidates.Add($onPath.Source) }
    $toolsRoot = Join-Path (Split-Path -Parent $repoRoot) '.tools'
    if (Test-Path -LiteralPath $toolsRoot) {
        foreach ($dir in Get-ChildItem -LiteralPath $toolsRoot -Directory -Filter 'winlibs-*' -ErrorAction SilentlyContinue) {
            $gccPath = Join-Path $dir.FullName 'mingw64\bin\gcc.exe'
            if (Test-Path -LiteralPath $gccPath) { $candidates.Add($gccPath) }
        }
    }
    foreach ($candidate in $candidates) {
        $resolved = $candidate
        if (-not (Test-Path -LiteralPath $resolved)) {
            $command = Get-Command $candidate -ErrorAction SilentlyContinue
            if (-not $command) { continue }
            $resolved = $command.Source
        }
        $version = & $resolved --version 2>&1
        if ($LASTEXITCODE -eq 0) {
            return [pscustomobject]@{ Path = $resolved; Version = [string](@($version)[0]) }
        }
    }
    throw @'
No C compiler found.

dist/occodebridge.dll cannot be produced without a C toolchain: the CPA native
plugin ABI requires "go build -buildmode=c-shared". This repository expects the
local portable winlibs MinGW-w64 install (D:\code\cpaexp\.tools\winlibs-*), a
gcc on PATH, or -Compiler <path to gcc.exe> / $env:CC. OCcodeBridge deliberately
does not download or install compilers.
'@
}

# The produced file must be a Windows amd64 PE, not a stale or foreign binary.
function Assert-WindowsAmd64Pe([string]$Path) {
    $bytes = [IO.File]::ReadAllBytes($Path)
    if ($bytes.Length -lt 0x40) { throw "$Path is too small to be a PE file." }
    $peOffset = [BitConverter]::ToInt32($bytes, 0x3c)
    if ($peOffset -le 0 -or $peOffset + 6 -ge $bytes.Length) { throw "$Path has no valid PE header." }
    if ([BitConverter]::ToUInt32($bytes, $peOffset) -ne 0x00004550) { throw "$Path is not a PE file." }
    $machine = [BitConverter]::ToUInt16($bytes, $peOffset + 4)
    if ($machine -ne 0x8664) { throw ("$Path targets PE machine 0x{0:x4}, not amd64 (0x8664)." -f $machine) }
}

Push-Location $repoRoot
try {
    $goCommand = Get-Command go -ErrorAction SilentlyContinue
    if (-not $goCommand) {
        throw 'Go toolchain not found on PATH. Install Go 1.26+ and re-run scripts/build-windows.ps1.'
    }
    $compiler = Resolve-Compiler $compilerArgument
    $version = Get-RegistryVersion

    $env:CGO_ENABLED = '1'
    $env:CC = $compiler.Path
    $env:GOOS = 'windows'
    $env:GOARCH = 'amd64'
    New-Item -ItemType Directory -Force -Path (Join-Path $repoRoot 'dist') | Out-Null

    Write-Output "Building $outputPath (version $version)"
    Write-Output "CC=$($compiler.Path)"
    Write-Output "compiler=$($compiler.Version)"
    & go build -trimpath -buildvcs=false -buildmode=c-shared -ldflags='-s -w' -o $outputPath ./cmd/occodebridge
    if ($LASTEXITCODE -ne 0) {
        throw "go build -buildmode=c-shared failed with exit code $LASTEXITCODE."
    }
    if (-not (Test-Path -LiteralPath $outputPath)) {
        throw "Build reported success but $outputPath is missing."
    }
    Assert-WindowsAmd64Pe $outputPath
    $artifact = Get-Item -LiteralPath $outputPath
    $hash = (Get-FileHash -LiteralPath $outputPath -Algorithm SHA256).Hash.ToLowerInvariant()
    Write-Output ("Built {0} ({1} bytes) sha256={2}" -f $artifact.FullName, $artifact.Length, $hash)

    if ($packageZip) {
        $archive = Join-Path $repoRoot ("dist\occodebridge_{0}_windows_amd64.zip" -f $version)
        if (Test-Path -LiteralPath $archive) { Remove-Item -LiteralPath $archive -Force }
        Compress-Archive -LiteralPath $artifact.FullName -DestinationPath $archive
        $zip = [IO.Compression.ZipFile]::OpenRead($archive)
        try {
            if ($zip.Entries.Count -ne 1 -or $zip.Entries[0].FullName -ne 'occodebridge.dll') {
                throw 'The package must contain occodebridge.dll at its root.'
            }
        } finally {
            $zip.Dispose()
        }
        Write-Output ("Packaged {0} ({1} bytes)" -f $archive, (Get-Item -LiteralPath $archive).Length)
    }
}
finally {
    Pop-Location
}
