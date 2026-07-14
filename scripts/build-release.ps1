param(
    [string]$Version = "1.1.0",
    [switch]$SkipNpmInstall
)

$ErrorActionPreference = "Stop"
$Root = Split-Path -Parent $PSScriptRoot
$Version = $Version.Trim()
if ($Version.StartsWith("v")) { $Version = $Version.Substring(1) }
if ($Version -notmatch '^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$') {
    throw "Version must be a stable semantic version such as 1.0.0"
}
$PreviousErrorActionPreference = $ErrorActionPreference
$ErrorActionPreference = "SilentlyContinue"
$Commit = (& git -C $Root rev-parse --verify --short HEAD 2>$null)
$GitExitCode = $LASTEXITCODE
$ErrorActionPreference = $PreviousErrorActionPreference
if ($GitExitCode -ne 0 -or -not $Commit) { $Commit = "unknown" }
$BuildTime = (Get-Date).ToUniversalTime().ToString("yyyy-MM-ddTHH:mm:ssZ")
$BaseLdFlags = "-s -w -X mailmanager/internal/version.Version=$Version -X mailmanager/internal/version.Commit=$Commit -X mailmanager/internal/version.BuildTime=$BuildTime"

function Assert-NativeSuccess([string]$Step) {
    if ($LASTEXITCODE -ne 0) {
        throw "$Step failed with exit code $LASTEXITCODE"
    }
}

if (-not $SkipNpmInstall) {
    npm ci --prefix (Join-Path $Root "web")
    Assert-NativeSuccess "npm ci"
}
npm run build --prefix (Join-Path $Root "web")
Assert-NativeSuccess "frontend build"
$Dist = Join-Path $Root "dist"
if (Test-Path -LiteralPath $Dist) {
    Get-ChildItem -LiteralPath $Dist -Force | Remove-Item -Recurse -Force
}
New-Item -ItemType Directory -Force $Dist | Out-Null

$PreviousGoEnvironment = @{}
foreach ($Name in @("CGO_ENABLED", "GOOS", "GOARCH")) {
    $Item = Get-Item -LiteralPath "Env:$Name" -ErrorAction SilentlyContinue
    $PreviousGoEnvironment[$Name] = @{ Exists = $null -ne $Item; Value = if ($Item) { $Item.Value } else { $null } }
}

try {
    $env:CGO_ENABLED = "0"
    $env:GOOS = "linux"
    $env:GOARCH = "amd64"
    $LdFlags = "$BaseLdFlags -X mailmanager/internal/version.BuildMarker=mailmanager-release:$Version`:linux:amd64"
    go build -C $Root -trimpath -ldflags $LdFlags -o dist/mailmanager-linux-amd64 ./cmd/mailmanager
    Assert-NativeSuccess "linux/amd64 build"

    $env:GOARCH = "arm64"
    $LdFlags = "$BaseLdFlags -X mailmanager/internal/version.BuildMarker=mailmanager-release:$Version`:linux:arm64"
    go build -C $Root -trimpath -ldflags $LdFlags -o dist/mailmanager-linux-arm64 ./cmd/mailmanager
    Assert-NativeSuccess "linux/arm64 build"

    Remove-Item Env:CGO_ENABLED, Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
    go run -C $Root ./scripts/verify-release.go (Join-Path $Dist "mailmanager-linux-amd64") $Version linux amd64
    Assert-NativeSuccess "linux/amd64 build verification"
    go run -C $Root ./scripts/verify-release.go (Join-Path $Dist "mailmanager-linux-arm64") $Version linux arm64
    Assert-NativeSuccess "linux/arm64 build verification"
}
finally {
    foreach ($Name in $PreviousGoEnvironment.Keys) {
        if ($PreviousGoEnvironment[$Name].Exists) {
            Set-Item -LiteralPath "Env:$Name" -Value $PreviousGoEnvironment[$Name].Value
        }
        else {
            Remove-Item -LiteralPath "Env:$Name" -ErrorAction SilentlyContinue
        }
    }
}

$ReleaseFiles = @{
    (Join-Path $Root "deploy/mailmanager.service") = "mailmanager.service"
    (Join-Path $Root "deploy/mailmanager-updater.service") = "mailmanager-updater.service"
    (Join-Path $Root "deploy/mailmanager-updater.path") = "mailmanager-updater.path"
    (Join-Path $Root "deploy/mailmanager.env.example") = "mailmanager.env.example"
    (Join-Path $Root "deploy/nginx.conf.example") = "nginx.conf.example"
    (Join-Path $Root "scripts/install-linux.sh") = "install-linux.sh"
}
foreach ($Source in $ReleaseFiles.Keys) {
    Copy-Item -LiteralPath $Source -Destination (Join-Path $Dist $ReleaseFiles[$Source])
}

$ChecksumLines = Get-ChildItem -LiteralPath $Dist -File |
    Sort-Object Name |
    ForEach-Object {
        $Hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $_.FullName).Hash.ToLowerInvariant()
        "$Hash  $($_.Name)"
    }
$Utf8NoBom = [System.Text.UTF8Encoding]::new($false)
[System.IO.File]::WriteAllText(
    (Join-Path $Dist "checksums.txt"),
    (($ChecksumLines -join "`n") + "`n"),
    $Utf8NoBom
)
