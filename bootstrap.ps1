#Requires -Version 5.1
[CmdletBinding()]
param(
    [string]$Fleet = (Join-Path $env:USERPROFILE "secondhand-fleet"),
    [switch]$Yes,
    [switch]$Check
)

$ErrorActionPreference = 'Stop'

$HAND_RELEASE_TAG = '@HAND_RELEASE_TAG@'
$HAND_RELEASE_VERSION = '@HAND_RELEASE_VERSION@'
$HAND_RELEASE_COMMIT = '@HAND_RELEASE_COMMIT@'
$HAND_RELEASE_RUNTIME_ID = '@HAND_RELEASE_RUNTIME_ID@'
$HAND_RELEASE_SHA256_WINDOWS_AMD64 = '@HAND_RELEASE_SHA256_WINDOWS_AMD64@'
$HAND_RELEASE_ASSET = 'hand-windows-amd64.zip'

function Write-BootstrapLog {
    param([string]$Message)
    Write-Host $Message
}

function Fail {
    param([string]$Message)
    Write-BootstrapLog "bootstrap.ps1: $Message"
    exit 1
}

$releasePlaceholderPrefix = '@HAND' + '_RELEASE_'
foreach ($value in @($HAND_RELEASE_TAG, $HAND_RELEASE_VERSION, $HAND_RELEASE_COMMIT, $HAND_RELEASE_RUNTIME_ID, $HAND_RELEASE_SHA256_WINDOWS_AMD64)) {
    if ($value -like "$releasePlaceholderPrefix*") {
        Fail 'this source template is not a release-bound bootstrap asset'
    }
    if ([string]::IsNullOrWhiteSpace($value)) {
        Fail 'a release binding is empty'
    }
}
if ($HAND_RELEASE_TAG -cne "v$HAND_RELEASE_VERSION") {
    Fail 'release tag and version do not agree'
}
if ($HAND_RELEASE_COMMIT -notmatch '^[0-9a-fA-F]{40}([0-9a-fA-F]{24})?$') {
    Fail 'release commit must be a full 40- or 64-character hexadecimal ID'
}
if ($HAND_RELEASE_SHA256_WINDOWS_AMD64 -notmatch '^[0-9a-fA-F]{64}$') {
    Fail "invalid embedded release digest for $HAND_RELEASE_ASSET"
}

$handInstallDir = if ($env:HAND_INSTALL_DIR) {
    $env:HAND_INSTALL_DIR
} else {
    $localAppData = if ($env:LOCALAPPDATA) {
        $env:LOCALAPPDATA
    } else {
        [Environment]::GetFolderPath('LocalApplicationData')
    }
    Join-Path $localAppData 'hand'
}
if (-not [IO.Path]::IsPathRooted($handInstallDir)) {
    Fail 'HAND_INSTALL_DIR must be an absolute path'
}
$handInstallDir = [IO.Path]::GetFullPath($handInstallDir)
$handTarget = Join-Path $handInstallDir 'hand.exe'

function Get-Sha256 {
    param([string]$Path)
    $sha256 = [System.Security.Cryptography.SHA256]::Create()
    try {
        $bytes = [System.IO.File]::ReadAllBytes($Path)
        return ([System.BitConverter]::ToString($sha256.ComputeHash($bytes)) -replace '-', '').ToLowerInvariant()
    } finally {
        $sha256.Dispose()
    }
}

function Get-Field {
    param([string]$Text, [string]$Name)
    foreach ($line in ($Text -split '\r?\n')) {
        if ($line -match ('^' + [regex]::Escape($Name) + ': (.*)$')) {
            $value = $Matches[1].Trim()
            if ($value.StartsWith('"') -and $value.EndsWith('"')) {
                return ($value | ConvertFrom-Json)
            }
            return $value
        }
    }
    return $null
}

function Invoke-HandCapture {
    param([string]$Path, [string]$HandHome, [string[]]$Arguments)
    $hadHome = Test-Path Env:HAND_HOME
    $previousHome = $env:HAND_HOME
    $previousErrorActionPreference = $ErrorActionPreference
    try {
        if ($null -eq $HandHome) {
            Remove-Item Env:HAND_HOME -ErrorAction SilentlyContinue
        } else {
            $env:HAND_HOME = $HandHome
        }
        $ErrorActionPreference = 'Continue'
        $lines = @(& $Path @Arguments 2>&1)
        $code = $LASTEXITCODE
        $output = $lines | Out-String
        return [pscustomobject]@{
            Code = $code
            Output = $output
        }
    } finally {
        $ErrorActionPreference = $previousErrorActionPreference
        if ($hadHome) {
            $env:HAND_HOME = $previousHome
        } else {
            Remove-Item Env:HAND_HOME -ErrorAction SilentlyContinue
        }
    }
}

function Assert-HandIdentity {
    param([string]$Path)
    $identity = Invoke-HandCapture $Path $null @('build-info')
    if ($identity.Code -ne 0) {
        Write-BootstrapLog $identity.Output
        Fail 'selected Hand executable failed its pure build identity query'
    }
    $expected = @{
        version = $HAND_RELEASE_VERSION
        channel = 'stable'
        commit = $HAND_RELEASE_COMMIT
        distribution = 'github'
    }
    foreach ($name in $expected.Keys) {
        $actual = Get-Field $identity.Output $name
        if ($name -eq 'commit') {
            if (-not [string]::Equals($actual, $expected[$name], [StringComparison]::OrdinalIgnoreCase)) {
                Write-BootstrapLog $identity.Output
                Fail "selected Hand $name does not match release $($expected[$name])"
            }
        } elseif ($actual -cne $expected[$name]) {
            Write-BootstrapLog $identity.Output
            Fail "selected Hand $name does not match release $($expected[$name])"
        }
    }
}

function Invoke-HandOutsideFleet {
    param([string]$Path, [string]$HandHome, [string[]]$Arguments)
    $location = Get-Location
    try {
        Set-Location ([IO.Path]::GetPathRoot($Path))
        return Invoke-HandCapture $Path $HandHome $Arguments
    } finally {
        Set-Location $location
    }
}

function Find-Hand {
    if (Test-Path -LiteralPath $handTarget -PathType Leaf) {
        return [IO.Path]::GetFullPath($handTarget)
    }
    $handCommand = Get-Command hand -ErrorAction SilentlyContinue
    if (-not $handCommand) {
        return $null
    }
    if ($handCommand.CommandType -ne 'Application' -or -not $handCommand.Path) {
        Fail 'hand is not an executable application; ownership is unknown (check mode: no changes made)'
    }
    return [IO.Path]::GetFullPath($handCommand.Path)
}

$handPath = $null
$handAvailable = $false

if ($Check) {
    $handPath = Find-Hand
    if ($handPath) {
        $handAvailable = $true
        $identity = Invoke-HandCapture $handPath $null @('build-info')
        if ($identity.Code -ne 0) {
            Write-BootstrapLog $identity.Output
            Fail 'hand build identity is unavailable (check mode: no changes made)'
        }
        Write-BootstrapLog 'hand identity (check mode: no changes made):'
        Write-BootstrapLog $identity.Output
        Write-BootstrapLog 'private runtime status (check mode: no changes made):'
        $runtimeStatus = Invoke-HandOutsideFleet $handPath $null @('runtime', 'status')
        Write-BootstrapLog $runtimeStatus.Output
    } else {
        Write-BootstrapLog 'hand: not installed (check mode: no changes made)'
    }
}

function Install-Hand {
    $tmp = Join-Path ([IO.Path]::GetTempPath()) ([IO.Path]::GetRandomFileName())
    New-Item -ItemType Directory -Path $tmp -Force | Out-Null
    try {
        $baseUrl = "https://github.com/atqamz/hand/releases/download/$HAND_RELEASE_TAG"
        $archive = Join-Path $tmp $HAND_RELEASE_ASSET
        try {
            Invoke-WebRequest -UseBasicParsing -Uri "$baseUrl/$HAND_RELEASE_ASSET" -OutFile $archive
        } catch {
            Fail ("download failed for the exact release {0}: {1}" -f $HAND_RELEASE_TAG, $_.Exception.Message)
        }
        if (-not (Test-Path -LiteralPath $archive -PathType Leaf) -or (Get-Item -LiteralPath $archive).Length -eq 0) {
            Fail 'downloaded Hand release archive is empty'
        }
        $got = Get-Sha256 $archive
        $want = $HAND_RELEASE_SHA256_WINDOWS_AMD64.ToLowerInvariant()
        if ($got -cne $want) {
            Fail "digest mismatch for $($HAND_RELEASE_ASSET): want $want, got $got"
        }

        Expand-Archive -LiteralPath $archive -DestinationPath $tmp -Force
        $handSource = Join-Path $tmp 'hand.exe'
        if (-not (Test-Path -LiteralPath $handSource -PathType Leaf)) {
            Fail 'verified release archive does not contain hand.exe'
        }

        $adoption = Invoke-HandCapture $handSource $null @(
            'adopt',
            '--source', $handSource,
            '--target', $handTarget,
            '--version', $HAND_RELEASE_VERSION,
            '--commit', $HAND_RELEASE_COMMIT
        )
        if ($adoption.Code -ne 0) {
            Write-BootstrapLog $adoption.Output
            Fail 'exact Hand adoption failed; no Fleet or runtime mutation was attempted'
        }
        Write-BootstrapLog $adoption.Output
        $selected = Get-Field $adoption.Output 'path'
        if ([string]::IsNullOrWhiteSpace($selected) -or -not [IO.Path]::IsPathRooted($selected)) {
            Fail 'exact Hand adoption returned no absolute executable path'
        }
        if (-not (Test-Path -LiteralPath $selected -PathType Leaf)) {
            Fail "selected Hand executable is missing: $selected"
        }
        Assert-HandIdentity $selected
        $script:handPath = $selected
        $script:handAvailable = $true
    } catch {
        if ($_.Exception.Message -like 'bootstrap.ps1:*') {
            throw
        }
        Fail "could not install Hand to $handInstallDir; resolve permissions without administrator access and rerun bootstrap.ps1: $($_.Exception.Message)"
    } finally {
        Remove-Item -LiteralPath $tmp -Recurse -Force -ErrorAction SilentlyContinue
    }
}

if (-not $Check) {
    Install-Hand
}

function Ensure-PrivateRuntime {
    if ($Check) {
        if (-not $handAvailable) {
            Write-BootstrapLog 'private runtime: not checked because Hand is absent (check mode: no changes made)'
        }
        return
    }
    if (-not $handAvailable) {
        Fail 'Hand was not installed'
    }
    $result = Invoke-HandOutsideFleet $handPath $null @('runtime', 'ensure')
    if ($result.Code -ne 0) {
        Write-BootstrapLog $result.Output
        Fail "private runtime is not ready; repair with: $handPath runtime ensure"
    }
    $runtimeID = Get-Field $result.Output 'runtime_id'
    if ($runtimeID -cne $HAND_RELEASE_RUNTIME_ID) {
        Write-BootstrapLog $result.Output
        Fail "private runtime identity mismatch: want $HAND_RELEASE_RUNTIME_ID, got $runtimeID"
    }
    Write-BootstrapLog "ensuring private pinned Git, Treehouse, and Herdr runtime for $HAND_RELEASE_VERSION ($HAND_RELEASE_RUNTIME_ID)"
    Write-BootstrapLog $result.Output
}

Ensure-PrivateRuntime

if ($Check) {
    if (-not $handAvailable) {
        Write-BootstrapLog 'fleet target: not checked because Hand is absent (check mode: no changes made)'
        exit 0
    }
    if (-not (Test-Path -LiteralPath $Fleet)) {
        Write-BootstrapLog "fleet target: $Fleet (absent; check mode: no changes made)"
        exit 0
    }
    $result = Invoke-HandCapture $handPath $Fleet @('doctor', '--fail-if-not-ready')
    Write-BootstrapLog $result.Output
    if ($result.Code -ne 0) {
        Fail "hand doctor reported that $Fleet is not ready"
    }
    exit 0
}

$result = Invoke-HandCapture $handPath $null @('init', $Fleet)
if ($result.Code -ne 0) {
    Write-BootstrapLog $result.Output
    Fail "hand init refused or failed for $Fleet; resolve the reported error, then rerun bootstrap.ps1 -Fleet $Fleet"
}
Write-BootstrapLog $result.Output

$result = Invoke-HandCapture $handPath $Fleet @('doctor', '--fail-if-not-ready')
if ($result.Code -ne 0) {
    Write-BootstrapLog $result.Output
    Fail ("hand doctor reported that {0} is not ready; rerun HAND_HOME='{1}' {2} doctor after recovery" -f $Fleet, $Fleet, $handPath)
}
Write-BootstrapLog $result.Output
