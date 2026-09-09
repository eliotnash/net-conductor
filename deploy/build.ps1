$ErrorActionPreference='Stop'
Push-Location (Join-Path $PSScriptRoot '..')
try {
 npm.cmd ci --no-audit --no-fund
 if ($LASTEXITCODE -ne 0) { throw 'npm install failed' }
 Copy-Item node_modules/@xterm/xterm/lib/xterm.js web/static/xterm.js -Force
 Copy-Item node_modules/@xterm/xterm/css/xterm.css web/static/xterm.css -Force
 Copy-Item node_modules/@xterm/addon-fit/lib/addon-fit.js web/static/addon-fit.js -Force
 go test ./...
 if ($LASTEXITCODE -ne 0) { throw 'Tests failed' }
 go vet ./...
 if ($LASTEXITCODE -ne 0) { throw 'Vet failed' }
 New-Item -ItemType Directory -Force dist | Out-Null
 $env:GOOS='windows'; $env:GOARCH='amd64'
 go build -buildvcs=false -o dist/netconductor.exe ./cmd/netconductor
 if ($LASTEXITCODE -ne 0) { throw 'Windows build failed' }
 $env:GOOS='linux'
 go build -buildvcs=false -o dist/netconductor-linux-amd64 ./cmd/netconductor
 if ($LASTEXITCODE -ne 0) { throw 'Linux build failed' }
 Push-Location desktop
 try { npm.cmd ci --no-audit --no-fund; npm.cmd run package; if ($LASTEXITCODE -ne 0) { throw 'Desktop packaging failed' } } finally { Pop-Location }
} finally { Remove-Item Env:GOOS -ErrorAction SilentlyContinue; Remove-Item Env:GOARCH -ErrorAction SilentlyContinue; Pop-Location }
