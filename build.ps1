# Builds the application, agent first, and puts
# "Music Library Organizer.exe" in the root of the project.
#
# The agent is the half that runs on the phone and it is embedded into the
# application, so building it second would ship the previous one — and the
# phone would keep reading tags with old code.
#
# Wails builds into a build folder it fills from packaging (the icon and the
# Windows manifest). That happens in a copy of the project in the temporary
# folder, so the project itself never gets a build folder.

$ErrorActionPreference = 'Stop'
Set-Location $PSScriptRoot

$agent = 'internal/device/agent/mlm-agent-arm64'
$exe = 'Music Library Organizer.exe'

Write-Host 'Building the phone agent...'
$env:CGO_ENABLED = '0'
$env:GOOS = 'linux'
$env:GOARCH = 'arm64'
go build -trimpath -ldflags='-s -w' -o $agent ./cmd/agent
if ($LASTEXITCODE -ne 0) { throw 'the agent did not build' }

Remove-Item Env:CGO_ENABLED, Env:GOOS, Env:GOARCH

Write-Host 'Building the application...'
$stage = Join-Path ([System.IO.Path]::GetTempPath()) 'music-library-organizer-build'
if (Test-Path $stage) { Remove-Item $stage -Recurse -Force }
New-Item -ItemType Directory $stage | Out-Null
Get-ChildItem -Force | Where-Object { $_.Name -notin '.git', 'build', 'dist', $exe } |
    Copy-Item -Destination $stage -Recurse -Force
New-Item -ItemType Directory (Join-Path $stage 'build') | Out-Null
Copy-Item (Join-Path 'packaging' '*') (Join-Path $stage 'build') -Recurse -Force

Push-Location $stage
try {
    wails build -platform windows/amd64
    if ($LASTEXITCODE -ne 0) { throw 'wails build failed' }
} finally {
    Pop-Location
}

Copy-Item (Join-Path $stage "build/bin/$exe") $exe -Force
Remove-Item $stage -Recurse -Force

Write-Host "Done: $exe"
