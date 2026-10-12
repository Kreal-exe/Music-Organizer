# Builds "Music Library Organizer.exe", puts it on the Desktop and opens it.
#
#   .\build.ps1
#
# $env:INSTALL_DIR = 'D:\Apps'; .\build.ps1   puts it somewhere else.
# Needs Go, nothing else: the interface in frontend\dist is plain HTML, CSS and
# JavaScript and is embedded as it is. The build happens in a copy of the
# project in the temporary folder, so nothing is written into the project.

$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

$exe = 'Music Library Organizer.exe'
$info = Get-Content wails.json -Raw | ConvertFrom-Json
$version = $info.info.productVersion

$stage = Join-Path ([System.IO.Path]::GetTempPath()) 'music-library-organizer-build'
if (Test-Path $stage) { Remove-Item $stage -Recurse -Force }
New-Item -ItemType Directory $stage | Out-Null
Get-ChildItem -Force | Where-Object { $_.Name -notin '.git', 'build', 'dist', $exe } |
    Copy-Item -Destination $stage -Recurse -Force

Push-Location $stage
try {
    # The phone agent is embedded into the application, so it is built first:
    # an application built against a stale agent reads tags on the phone with
    # old code.
    Write-Host "Building $($info.info.productName) $version..."
    $env:CGO_ENABLED = '0'; $env:GOOS = 'linux'; $env:GOARCH = 'arm64'
    go build -trimpath -buildvcs=false -ldflags='-s -w' -o internal/device/agent/mlm-agent-arm64 ./cmd/agent
    if ($LASTEXITCODE -ne 0) { throw 'the agent did not build' }
    Remove-Item Env:CGO_ENABLED, Env:GOOS, Env:GOARCH

    # The icon, the version and the manifest (common controls, per-monitor DPI)
    # go into a resource file that go build links in by itself. The icon is
    # stored twice: Explorer takes the first one, and Wails loads the window's
    # own icon by resource ID 3 (packaging\windows\winres.json).
    go run github.com/tc-hib/go-winres@v0.3.3 make --in packaging/windows/winres.json --arch amd64 `
        --product-version $version --file-version $version
    if ($LASTEXITCODE -ne 0) { throw 'the resources did not build' }

    # "desktop,production" are the tags `wails build` passes for a release build.
    $env:GOOS = 'windows'; $env:GOARCH = 'amd64'
    go build -trimpath -buildvcs=false -tags desktop,production -ldflags='-w -s -H windowsgui' -o $exe .
    if ($LASTEXITCODE -ne 0) { throw 'the application did not build' }
} finally {
    Remove-Item Env:CGO_ENABLED, Env:GOOS, Env:GOARCH -ErrorAction SilentlyContinue
    Pop-Location
}

$dest = if ($env:INSTALL_DIR) { $env:INSTALL_DIR } else { [Environment]::GetFolderPath('Desktop') }
$installed = Join-Path $dest $exe
# Close a running copy first, so the file can be replaced.
Get-Process 'Music Library Organizer' -ErrorAction SilentlyContinue | Where-Object { $_.Path -eq $installed } |
    ForEach-Object { $_.CloseMainWindow() | Out-Null; $_.WaitForExit(10000) | Out-Null }
New-Item -ItemType Directory -Force $dest | Out-Null
Copy-Item (Join-Path $stage $exe) $installed -Force
Remove-Item $stage -Recurse -Force
Write-Host "Installed: $installed"
if (-not $env:CI) { Start-Process $installed }   # opened right away, except on a CI runner
