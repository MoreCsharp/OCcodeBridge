$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest

# One-shot repository check for OCcodeBridge: format, vet, build, Go tests, console tests.
#
# Nothing here needs a C compiler, network access or a real API key: the cgo
# entry point in cmd/occodebridge is excluded when CGO_ENABLED=0, so the business
# packages stay testable anywhere. Use scripts/build-windows.ps1 for the native
# plugin build.
#
#   pwsh -NoProfile -File scripts/check.ps1

$repoRoot = Split-Path -Parent $PSScriptRoot

function Invoke-Step {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][scriptblock]$Action
    )
    Write-Output "== $Name"
    & $Action
    if ($LASTEXITCODE -ne 0) {
        throw "$Name failed with exit code $LASTEXITCODE"
    }
}

Push-Location $repoRoot
try {
    Invoke-Step 'go version' { & go version }
    Invoke-Step 'go mod verify' { & go mod verify }

    Write-Output '== gofmt -l internal cmd'
    $unformatted = & gofmt -l internal cmd
    if ($LASTEXITCODE -ne 0) {
        throw "gofmt failed with exit code $LASTEXITCODE"
    }
    if ($unformatted) {
        throw "These files are not gofmt clean:`n$($unformatted -join "`n")"
    }

    # The plugin entry point imports "C"; with CGO_ENABLED=0 it is excluded from
    # these packages, so vet/build/test work without a C toolchain.
    $env:CGO_ENABLED = '0'
    Invoke-Step 'go vet' { & go vet ./... }
    Invoke-Step 'go build' { & go build ./... }
    Invoke-Step 'go test' { & go test -count=1 -timeout=180s ./... }

    Write-Output '== node --test tests/*.test.cjs'
    & node --test tests/*.test.cjs
    if ($LASTEXITCODE -ne 0) {
        throw "node --test failed with exit code $LASTEXITCODE"
    }
    Write-Output 'All checks passed.'
}
finally {
    Pop-Location
}
