$ErrorActionPreference='Stop'
Set-StrictMode -Version Latest

# Native acceptance: run one real CLIProxyAPI host with the freshly built
# PlanBridge DLL and drive a complete request through it.
#
# Everything happens under one isolated runtime directory (default
# D:\code\cpaexp\.runtime\planbridge-e2e, override with PLANBRIDGE_E2E_ROOT):
# the CPA binary, its config/auth/logs, the plugin data directory, and a local
# fake OpenCode Go upstream. No request leaves 127.0.0.1, no real key is used,
# and both processes are stopped at the end.
#
#   pwsh -NoProfile -File scripts/e2e-native.ps1
#
# The CPA executable is reused when bin\cli-proxy-api.exe already exists; set
# PLANBRIDGE_CPA_EXE to seed it from a verified build, or leave it unset to build
# ./cmd/server from PLANBRIDGE_CPA_SOURCE (default D:\code\cpaexp\CLIProxyAPI).

$repoRoot = Split-Path -Parent $PSScriptRoot
$runtimeRoot = if ($env:PLANBRIDGE_E2E_ROOT) { $env:PLANBRIDGE_E2E_ROOT } else { 'D:\code\cpaexp\.runtime\planbridge-e2e' }
$cpaSource = if ($env:PLANBRIDGE_CPA_SOURCE) { $env:PLANBRIDGE_CPA_SOURCE } else { 'D:\code\cpaexp\CLIProxyAPI' }
$utf8 = New-Object System.Text.UTF8Encoding($false)

$binDir = Join-Path $runtimeRoot 'bin'
$logDir = Join-Path $runtimeRoot 'logs'
$pluginDir = Join-Path $runtimeRoot 'plugins'
$authDir = Join-Path $runtimeRoot 'auths'
$dataDir = Join-Path $runtimeRoot 'data'
$stateDir = Join-Path $runtimeRoot 'state'
$configPath = Join-Path $runtimeRoot 'config.yaml'
$capturePath = Join-Path $stateDir 'upstream-capture.jsonl'
$cpaExe = Join-Path $binDir 'cli-proxy-api.exe'

$clientKey = 'planbridge-e2e-client-' + [guid]::NewGuid().ToString('N')
$managementKey = 'planbridge-e2e-mgmt-' + [guid]::NewGuid().ToString('N')
$credentialKey = 'planbridge-e2e-upstream-' + [guid]::NewGuid().ToString('N')
$alias = 'e2e-deepseek-flash'
$upstreamModel = 'deepseek-v4.1-flash'

$results = [ordered]@{}
$cpaProcess = $null
$upstreamProcess = $null

function Write-Line([string]$text) { [Console]::Out.WriteLine($text) }

function Add-Result([string]$name, [bool]$ok, [string]$detail) {
    $label = if ($ok) { 'PASS' } else { 'FAIL' }
    Write-Line ("[{0}] {1} - {2}" -f $label, $name, $detail)
    $script:results[$name] = [ordered]@{ ok = $ok; detail = $detail }
}

# Keeps a secret out of the console: only a length and a short prefix survive.
function Mask-Secret([string]$value) {
    if ([string]::IsNullOrEmpty($value)) { return '<empty>' }
    if ($value.Length -le 8) { return '***' }
    return $value.Substring(0, 8) + '***(' + $value.Length + ' chars)'
}

function Get-FreePort {
    $listener = New-Object System.Net.Sockets.TcpListener([System.Net.IPAddress]::Loopback, 0)
    $listener.Start()
    try { return ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port } finally { $listener.Stop() }
}

function Invoke-Http {
    param(
        [Parameter(Mandatory = $true)][string]$Method,
        [Parameter(Mandatory = $true)][string]$Url,
        $Body = $null,
        [hashtable]$Headers = @{},
        [int]$TimeoutSec = 60
    )
    $params = @{ Method = $Method; Uri = $Url; Headers = $Headers; SkipHttpErrorCheck = $true; TimeoutSec = $TimeoutSec; NoProxy = $true }
    if ($null -ne $Body) {
        $params.ContentType = 'application/json'
        $params.Body = [Text.Encoding]::UTF8.GetBytes(($Body | ConvertTo-Json -Depth 20 -Compress))
    }
    $response = Invoke-WebRequest @params
    $text = [string]$response.Content
    $json = $null
    try { $json = $text | ConvertFrom-Json -Depth 30 } catch { }
    return [pscustomobject]@{ Status = [int]$response.StatusCode; Text = $text; Json = $json }
}

function Wait-For([scriptblock]$Probe, [int]$Seconds, [int]$IntervalMs = 500) {
    $deadline = (Get-Date).AddSeconds($Seconds)
    while ((Get-Date) -lt $deadline) {
        $value = & $Probe
        if ($value) { return $value }
        Start-Sleep -Milliseconds $IntervalMs
    }
    return $null
}

function Test-PortOpen([int]$Port) {
    $client = New-Object System.Net.Sockets.TcpClient
    try {
        $async = $client.BeginConnect('127.0.0.1', $Port, $null, $null)
        if (-not $async.AsyncWaitHandle.WaitOne(300)) { return $false }
        $client.EndConnect($async)
        return $true
    } catch { return $false } finally { $client.Dispose() }
}

function Resolve-CpaExecutable {
    if (Test-Path -LiteralPath $cpaExe) {
        Write-Line "reusing CPA executable $cpaExe"
        return
    }
    if ($env:PLANBRIDGE_CPA_EXE) {
        if (-not (Test-Path -LiteralPath $env:PLANBRIDGE_CPA_EXE)) {
            throw "PLANBRIDGE_CPA_EXE does not exist: $($env:PLANBRIDGE_CPA_EXE)"
        }
        New-Item -ItemType Directory -Force -Path $binDir | Out-Null
        Copy-Item -LiteralPath $env:PLANBRIDGE_CPA_EXE -Destination $cpaExe -Force
        Write-Line "seeded CPA executable from $($env:PLANBRIDGE_CPA_EXE)"
        return
    }
    if (-not (Test-Path -LiteralPath (Join-Path $cpaSource 'go.mod'))) {
        throw "no CPA executable at $cpaExe, PLANBRIDGE_CPA_EXE is unset, and $cpaSource is not a CPA source tree"
    }
    Write-Line "building CPA from source $cpaSource"
    New-Item -ItemType Directory -Force -Path $binDir | Out-Null
    Push-Location $cpaSource
    try {
        & go build -trimpath -buildvcs=false -o $cpaExe ./cmd/server
        if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $cpaExe)) {
            throw "building CPA from $cpaSource failed (exit $LASTEXITCODE); a matching release build can be reused with PLANBRIDGE_CPA_EXE"
        }
    } finally {
        Pop-Location
    }
}

# The CPA child gets a loopback-only environment: no ambient proxy may sit
# between the plugin and the fake upstream, and the management key arrives
# through MANAGEMENT_PASSWORD instead of a bcrypt hash in the config file.
# HTTP(S)_PROXY points at a closed loopback port so any request the plugin still
# aims at a real vendor host (the OpenCode Go usage endpoint is not part of the
# fake upstream) fails locally instead of reaching the internet.
function Start-CpaProcess([string]$config, [string]$stdoutLog, [string]$stderrLog, [int]$DeadProxyPort) {
    $saved = @{}
    foreach ($name in 'MANAGEMENT_PASSWORD', 'HTTP_PROXY', 'HTTPS_PROXY', 'ALL_PROXY', 'http_proxy', 'https_proxy', 'all_proxy', 'NO_PROXY') {
        $saved[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
    }
    try {
        Remove-Item -LiteralPath 'Env:ALL_PROXY' -ErrorAction SilentlyContinue
        Remove-Item -LiteralPath 'Env:all_proxy' -ErrorAction SilentlyContinue
        $env:NO_PROXY = '127.0.0.1,localhost,::1'
        $env:HTTP_PROXY = "http://127.0.0.1:$DeadProxyPort"
        $env:HTTPS_PROXY = "http://127.0.0.1:$DeadProxyPort"
        $env:MANAGEMENT_PASSWORD = $managementKey
        return Start-Process -FilePath $cpaExe -ArgumentList @('--config', $config, '--local-model') `
            -WorkingDirectory $runtimeRoot -WindowStyle Hidden -PassThru `
            -RedirectStandardOutput $stdoutLog -RedirectStandardError $stderrLog
    } finally {
        foreach ($name in $saved.Keys) {
            if ($null -eq $saved[$name]) {
                Remove-Item -LiteralPath "Env:$name" -ErrorAction SilentlyContinue
            } else {
                [Environment]::SetEnvironmentVariable($name, [string]$saved[$name], 'Process')
            }
        }
    }
}

function New-ConfigYaml([int]$cpaPort, [int]$upstreamPort) {
    $lines = @(
        '# Generated by scripts/e2e-native.ps1 for one local acceptance run.',
        'config-version: 8',
        'server:',
        '    host: "127.0.0.1"',
        ('    port: {0}' -f $cpaPort),
        '    response-compression:',
        '        enabled: false',
        'plugins:',
        '    enabled: true',
        ('    dir: "{0}"' -f ($pluginDir -replace '\\', '/')),
        '    configs:',
        '        planbridge:',
        '            enabled: true',
        '            priority: 0',
        ('            data_dir: "{0}"' -f ($dataDir -replace '\\', '/')),
        '            # Native non-stream keeps the fake upstream non-stream branch exercised.',
        '            nonstream_mode: native',
        '            base_url_overrides:',
        ('                opencode-go: "http://127.0.0.1:{0}/v1"' -f $upstreamPort),
        'routing:',
        '    strategy: "round-robin"',
        '    session-affinity: false',
        '    cooldown:',
        '        disable-cooling: true',
        '    retry:',
        '        request-retry: 0',
        '        max-retry-credentials: 1',
        'access:',
        '    api-keys:',
        ('        - "{0}"' -f $clientKey),
        'oauth:',
        ('    auth-dir: "{0}"' -f ($authDir -replace '\\', '/')),
        'management:',
        '    allow-remote: false',
        '    disable-control-panel: true',
        '    disable-auto-update-panel: true',
        'observability:',
        '    logs:',
        '        logging-to-file: true',
        '        request-log: false',
        '        debug: false'
    )
    return ($lines -join "`n") + "`n"
}

$summary = $null
try {
    New-Item -ItemType Directory -Force -Path $runtimeRoot, $binDir, $logDir, $stateDir | Out-Null

    # Every run starts from empty plugin state: auth files, plugin data and the
    # plugin directory are rebuilt, so a rerun cannot lean on the previous one.
    foreach ($directory in $pluginDir, $authDir, $dataDir) {
        if (Test-Path -LiteralPath $directory) { Remove-Item -LiteralPath $directory -Recurse -Force }
        New-Item -ItemType Directory -Force -Path $directory | Out-Null
    }
    Get-ChildItem -LiteralPath $logDir -File -ErrorAction SilentlyContinue | Remove-Item -Force

    Write-Line '== build the plugin DLL'
    & pwsh -NoProfile -File (Join-Path $PSScriptRoot 'build-windows.ps1')
    if ($LASTEXITCODE -ne 0) { throw "scripts/build-windows.ps1 failed with exit code $LASTEXITCODE" }
    Copy-Item -LiteralPath (Join-Path $repoRoot 'dist\planbridge.dll') -Destination (Join-Path $pluginDir 'planbridge.dll') -Force
    $dll = Get-Item -LiteralPath (Join-Path $pluginDir 'planbridge.dll')
    Write-Line ("plugin dll: {0} bytes sha256={1}" -f $dll.Length, (Get-FileHash -LiteralPath $dll.FullName -Algorithm SHA256).Hash.ToLowerInvariant())

    Write-Line '== resolve the CPA executable'
    Resolve-CpaExecutable
    $cpaInfo = Get-Item -LiteralPath $cpaExe
    Write-Line ("cpa exe: {0} bytes sha256={1}" -f $cpaInfo.Length, (Get-FileHash -LiteralPath $cpaExe -Algorithm SHA256).Hash.ToLowerInvariant())

    $cpaPort = Get-FreePort
    $upstreamPort = Get-FreePort
    $deadProxyPort = Get-FreePort
    $base = "http://127.0.0.1:$cpaPort"
    $managementHeaders = @{ Authorization = "Bearer $managementKey" }
    $clientHeaders = @{ Authorization = "Bearer $clientKey" }
    Write-Line "cpa=$base upstream=http://127.0.0.1:$upstreamPort"
    Write-Line "client key: $(Mask-Secret $clientKey); management key: $(Mask-Secret $managementKey); test upstream key: $(Mask-Secret $credentialKey)"

    Write-Line '== start the fake OpenCode Go upstream'
    $upstreamOut = Join-Path $logDir 'upstream-stdout.log'
    $upstreamErr = Join-Path $logDir 'upstream-stderr.log'
    $upstreamProcess = Start-Process -FilePath 'python' `
        -ArgumentList @((Join-Path $PSScriptRoot 'fake-opencode-upstream.py'), '--port', "$upstreamPort", '--capture', $capturePath) `
        -WorkingDirectory $runtimeRoot -WindowStyle Hidden -PassThru `
        -RedirectStandardOutput $upstreamOut -RedirectStandardError $upstreamErr
    $upstreamReady = Wait-For -Seconds 30 -Probe { Test-PortOpen -Port $upstreamPort }
    if (-not $upstreamReady) { throw "the fake upstream did not listen on 127.0.0.1:$upstreamPort; see $upstreamErr" }

    Write-Line '== write the isolated CPA config'
    [IO.File]::WriteAllText($configPath, (New-ConfigYaml -cpaPort $cpaPort -upstreamPort $upstreamPort), $utf8)

    Write-Line '== start CPA'
    $cpaStdout = Join-Path $logDir 'cpa-stdout.log'
    $cpaStderr = Join-Path $logDir 'cpa-stderr.log'
    $cpaProcess = Start-CpaProcess -config $configPath -stdoutLog $cpaStdout -stderrLog $cpaStderr -DeadProxyPort $deadProxyPort
    Write-Line "cpa pid=$($cpaProcess.Id)"
    $healthy = Wait-For -Seconds 60 -Probe {
        if ($cpaProcess.HasExited) { return $false }
        try { return ((Invoke-Http -Method GET -Url "$base/healthz" -TimeoutSec 5).Status -eq 200) } catch { return $false }
    }
    Add-Result 'cpa.starts' ([bool]$healthy) $(if ($healthy) { "GET /healthz -> 200 (pid $($cpaProcess.Id))" } else { "CPA did not answer /healthz; see $cpaStdout / $cpaStderr" })
    if (-not $healthy) { throw 'CPA did not start; the remaining assertions cannot run' }

    # 1. The host loaded the plugin: the console resource is served by the plugin
    #    resource route, and the plugin answers its own management API.
    $console = Invoke-Http -Method GET -Url "$base/v0/resource/plugins/planbridge/console"
    $consoleServed = $console.Status -eq 200 -and $console.Text.Contains('id="credentialsTab"')
    $consoleOk = $consoleServed -and -not $console.Text.Contains('__PLANBRIDGE_API_BASE__')
    Add-Result 'plugin.console' $consoleOk ("GET /v0/resource/plugins/planbridge/console -> {0}, {1} bytes, placeholders replaced={2}" -f $console.Status, $console.Text.Length, (-not $console.Text.Contains('__PLANBRIDGE_API_BASE__')))

    $status = Invoke-Http -Method GET -Url "$base/v0/management/planbridge/status" -Headers $managementHeaders
    $pluginVersion = if ($status.Json) { [string]$status.Json.version } else { '' }
    Add-Result 'plugin.loaded' ($status.Status -eq 200 -and $pluginVersion -eq '0.1.0') ("GET /v0/management/planbridge/status -> {0} version={1}" -f $status.Status, $pluginVersion)

    # 2. Import one credential through the plugin's own management API and add
    #    the model mapping a client asks for.
    $imported = Invoke-Http -Method POST -Url "$base/v0/management/planbridge/credentials" -Headers $managementHeaders -Body @{
        label              = 'e2e OpenCode Go'
        api_key            = $credentialKey
        vendor             = 'opencode-go'
        priority           = 50
    }
    $credentialId = if ($imported.Json) { [string]$imported.Json.id } else { '' }
    Add-Result 'credential.import' ($imported.Status -eq 201 -and $credentialId) ("POST /credentials -> {0} id={1}" -f $imported.Status, $credentialId)

    $models = Invoke-Http -Method PUT -Url "$base/v0/management/planbridge/models" -Headers $managementHeaders -Body @{
        models = @(@{ id = $alias; upstream_id = "opencode-go/$upstreamModel" })
    }
    Add-Result 'models.mapping' ($models.Status -eq 200) ("PUT /models -> {0} ({1})" -f $models.Status, $alias + ' -> opencode-go/' + $upstreamModel)

    $advertised = Wait-For -Seconds 30 -Probe {
        $response = Invoke-Http -Method GET -Url "$base/v1/models" -Headers $clientHeaders -TimeoutSec 10
        if ($response.Status -ne 200 -or $null -eq $response.Json) { return $null }
        $ids = @($response.Json.data | ForEach-Object { [string]$_.id })
        if ($ids -contains $alias) { return $response }
        return $null
    }
    Add-Result 'cpa.models' ([bool]$advertised) $(if ($advertised) { "GET /v1/models exposes $alias" } else { "GET /v1/models never exposed $alias" })

    $authFiles = Wait-For -Seconds 30 -Probe {
        $response = Invoke-Http -Method GET -Url "$base/v0/management/auth-files" -Headers $managementHeaders -TimeoutSec 10
        if ($response.Status -ne 200 -or $null -eq $response.Json) { return $null }
        $entry = @($response.Json.files | Where-Object { [string]$_.provider -eq 'planbridge' }) | Select-Object -First 1
        if ($entry -and [string]$entry.auth_index) { return $entry }
        return $null
    }
    $authIndex = if ($authFiles) { [string]$authFiles.auth_index } else { '' }
    Add-Result 'cpa.auth-record' ([bool]$authIndex) $(if ($authIndex) { "auth-files lists a planbridge record with auth_index" } else { 'no planbridge auth record appeared' })

    # 3. One non-stream and one stream client request through CPA.
    $chatBody = @{ model = $alias; messages = @(@{ role = 'user'; content = 'Reply with the fake upstream answer.' }) }
    $nonStream = Invoke-Http -Method POST -Url "$base/v1/chat/completions" -Headers $clientHeaders -Body $chatBody -TimeoutSec 120
    $nonStreamText = if ($nonStream.Json -and @($nonStream.Json.choices).Count -gt 0) { [string]$nonStream.Json.choices[0].message.content } else { '' }
    $nonStreamContentOk = $nonStream.Status -eq 200 -and $nonStreamText.Contains('FAKE-UPSTREAM-OK')
    $nonStreamHasUsage = ($null -ne $nonStream.Json) -and ($null -ne $nonStream.Json.usage)
    Add-Result 'chat.nonstream' $nonStreamContentOk ("POST /v1/chat/completions (non-stream) -> {0} content={1} usage_present={2}" -f $nonStream.Status, $nonStreamText, $nonStreamHasUsage)

    $streamBody = @{ model = $alias; stream = $true; messages = @(@{ role = 'user'; content = 'Reply with the fake upstream answer.' }) }
    $streamResponse = Invoke-WebRequest -Method POST -Uri "$base/v1/chat/completions" -Headers $clientHeaders -ContentType 'application/json' `
        -Body ([Text.Encoding]::UTF8.GetBytes(($streamBody | ConvertTo-Json -Depth 10 -Compress))) -SkipHttpErrorCheck -TimeoutSec 120 -NoProxy
    $streamText = [string]$streamResponse.Content
    $streamOk = [int]$streamResponse.StatusCode -eq 200 -and $streamText.Contains('FAKE-UPSTREAM-OK') -and $streamText.Contains('[DONE]')
    Add-Result 'chat.stream' $streamOk ("POST /v1/chat/completions (stream) -> {0}, contains answer={1}, [DONE]={2}" -f [int]$streamResponse.StatusCode, $streamText.Contains('FAKE-UPSTREAM-OK'), $streamText.Contains('[DONE]'))

    # 4. The plugin request log records the request and the plan allowance.
    $logResponse = Invoke-Http -Method GET -Url "$base/v0/management/planbridge/logs?limit=10" -Headers $managementHeaders
    $entries = if ($logResponse.Json) { @($logResponse.Json.items) } else { @() }
    $successful = @($entries | Where-Object { [int]$_.status -eq 200 })
    $withAllowance = @($successful | Where-Object { $null -ne $_.PSObject.Properties['allowance'] })
    $firstAllowance = if ($withAllowance.Count -gt 0) { $withAllowance[0].allowance } else { $null }
    $allowanceText = if ($null -ne $firstAllowance) { ($firstAllowance | ConvertTo-Json -Compress -Depth 5) } else { '' }
    $promptTokens = if ($successful.Count -gt 0) { [int64]$successful[0].prompt_tokens } else { 0 }
    $completionTokens = if ($successful.Count -gt 0) { [int64]$successful[0].completion_tokens } else { 0 }
    $upstreamLogged = if ($successful.Count -gt 0) { [string]$successful[0].upstream_model } else { '' }
    Add-Result 'plugin.request-log' ($successful.Count -ge 2 -and $withAllowance.Count -ge 1 -and $promptTokens -gt 0 -and $completionTokens -gt 0) `
        ("{0} logged request(s), {1} with allowance, tokens={2}/{3}, upstream_model={4}" -f $successful.Count, $withAllowance.Count, $promptTokens, $completionTokens, $upstreamLogged)
    Add-Result 'plugin.log-allowance' ($allowanceText -ne '') ("logged allowance: $allowanceText")

    # 5. What the fake upstream actually received: our test key, a stable
    #    session header, the plugin User-Agent, and the model without the prefix.
    $captured = @()
    if (Test-Path -LiteralPath $capturePath) {
        foreach ($line in [IO.File]::ReadAllLines($capturePath, $utf8)) {
            if ([string]::IsNullOrWhiteSpace($line)) { continue }
            $captured += ($line | ConvertFrom-Json -Depth 20)
        }
    }
    $capturedOk = $captured.Count -ge 2
    Add-Result 'upstream.captured' $capturedOk ("fake upstream received {0} request(s)" -f $captured.Count)

    if ($captured.Count -gt 0) {
        $authorization = [string]$captured[0].headers.Authorization
        $session = [string]$captured[0].headers.'x-opencode-session'
        $userAgent = [string]$captured[0].headers.'User-Agent'
        $sentModel = [string]$captured[0].body.model
        Add-Result 'upstream.authorization' ($authorization -eq "Bearer $credentialKey") ("Authorization={0} matches the test key" -f (Mask-Secret ($authorization -replace '^Bearer ', '')))
        Add-Result 'upstream.session-header' ($session.Length -gt 0) ("x-opencode-session={0}" -f $session)
        Add-Result 'upstream.user-agent' ($userAgent -like 'PlanBridge/0.1.0*') ("User-Agent=$userAgent")
        Add-Result 'upstream.model-prefix' ($sentModel -eq $upstreamModel) ("body.model=$sentModel (vendor prefix removed)")
        $sessions = @($captured | ForEach-Object { [string]$_.headers.'x-opencode-session' } | Sort-Object -Unique)
        Add-Result 'upstream.stable-session' ($sessions.Count -eq 1 -and $sessions[0].Length -gt 0) ("same session id across requests={0}" -f ($sessions.Count -eq 1))
        $streamFlags = @($captured | ForEach-Object { [bool]$_.body.stream })
        Add-Result 'upstream.both-modes' ($streamFlags -contains $true -and $streamFlags -contains $false) ("upstream saw stream={0}" -f (($streamFlags | ForEach-Object { $_.ToString().ToLowerInvariant() }) -join ','))
    }

    # 6. CPA's normalized quota route for the same credential. The fake upstream
    #    does not serve the vendor usage endpoint, so the only acceptable answer
    #    is an explicit error, and the host must stay healthy afterwards.
    if ($authIndex) {
        $quota = Invoke-Http -Method POST -Url "$base/v8/management/plugins/planbridge/quota" -Headers $managementHeaders -Body @{ auth_index = $authIndex } -TimeoutSec 120
        $quotaError = if ($quota.Json) { [string]$quota.Json.error } else { $quota.Text }
        $quotaExplicit = $quota.Status -ge 400 -and -not [string]::IsNullOrWhiteSpace($quotaError)
        Add-Result 'quota.explicit-error' $quotaExplicit ("POST /v8/management/plugins/planbridge/quota -> {0} error={1}" -f $quota.Status, $quotaError)

        $afterQuota = Invoke-Http -Method POST -Url "$base/v1/chat/completions" -Headers $clientHeaders -Body $chatBody -TimeoutSec 120
        $afterText = if ($afterQuota.Json -and @($afterQuota.Json.choices).Count -gt 0) { [string]$afterQuota.Json.choices[0].message.content } else { '' }
        Add-Result 'quota.host-survived' ($afterQuota.Status -eq 200 -and $afterText.Contains('FAKE-UPSTREAM-OK')) ("a chat request after the quota error -> {0}" -f $afterQuota.Status)
    }
}
finally {
    Write-Line '== stopping processes'
    if ($null -ne $cpaProcess) {
        try { if (-not $cpaProcess.HasExited) { Stop-Process -Id $cpaProcess.Id -Force; $null = $cpaProcess.WaitForExit(15000) } } catch { Write-Line "could not stop CPA: $($_.Exception.Message)" }
    }
    if ($null -ne $upstreamProcess) {
        try { if (-not $upstreamProcess.HasExited) { Stop-Process -Id $upstreamProcess.Id -Force; $null = $upstreamProcess.WaitForExit(15000) } } catch { Write-Line "could not stop the fake upstream: $($_.Exception.Message)" }
    }
    $summary = $results
}

# The host writes its startup log while it holds the redirection files open, so
# this one assertion is evaluated after both processes have stopped.
$logText = ''
foreach ($file in Get-ChildItem -Path $logDir -File -Filter *.log -ErrorAction SilentlyContinue) {
    try { $logText += [IO.File]::ReadAllText($file.FullName, $utf8) } catch { }
}
$hostLogOk = $logText -match '(?i)pluginhost: plugin loaded plugin_id=planbridge'
$loadedAndRegistered = $hostLogOk -and ($logText -match '(?i)pluginhost: plugin registered plugin_id=planbridge')
Add-Result 'plugin.host-log' $loadedAndRegistered ("CPA log records plugin load={0} register={1}" -f $hostLogOk, $loadedAndRegistered)
$summary = $results

$failed = @($summary.Keys | Where-Object { -not $summary[$_].ok })
Write-Line ''
Write-Line '== summary'
foreach ($name in $summary.Keys) {
    $label = if ($summary[$name].ok) { 'PASS' } else { 'FAIL' }
    Write-Line ("  [{0}] {1} - {2}" -f $label, $name, $summary[$name].detail)
}
Write-Line ("logs and state: {0}" -f $runtimeRoot)
if ($failed.Count -gt 0) {
    Write-Line ("RESULT: FAIL ({0}/{1} assertions failed)" -f $failed.Count, $summary.Count)
    exit 1
}
Write-Line ("RESULT: PASS ({0}/{1} assertions)" -f $summary.Count, $summary.Count)
exit 0
