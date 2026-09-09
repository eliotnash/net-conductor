#Requires -RunAsAdministrator
param([string]$DesktopUser=$env:USERNAME)
$ErrorActionPreference='Stop'
if (-not (Test-Path -LiteralPath (Join-Path $env:ProgramFiles 'WireGuard\wireguard.exe'))) {
 $msi=Join-Path $env:TEMP 'net-conductor-wireguard-amd64-1.1.msi'
 Invoke-WebRequest -Uri 'https://download.wireguard.com/windows-client/wireguard-amd64-1.1.msi' -OutFile $msi
 $signature=Get-AuthenticodeSignature -LiteralPath $msi
 if ($signature.Status -ne 'Valid' -or $signature.SignerCertificate.Subject -notmatch 'WireGuard') { throw 'WireGuard installer signature verification failed' }
 $installation=Start-Process msiexec.exe -ArgumentList @('/i',('"'+$msi+'"'),'/qn','/norestart','DO_NOT_LAUNCH=1') -WindowStyle Hidden -Wait -PassThru
 if ($installation.ExitCode -notin 0,3010) { throw "WireGuard installation failed: $($installation.ExitCode)" }
}
& (Join-Path $PSScriptRoot 'Install-Agent.ps1') -DesktopUser $DesktopUser
$desktopRoot=Join-Path $env:ProgramFiles 'NetConductorDesktop'
$desktopExe=Join-Path $desktopRoot 'NetConductor.exe'
Get-Process | Where-Object { $_.Path -eq $desktopExe } | ForEach-Object {
 Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue
 Wait-Process -Id $_.Id -Timeout 15 -ErrorAction SilentlyContinue
}
New-Item -ItemType Directory -Force $desktopRoot | Out-Null
Copy-Item -Path (Join-Path $PSScriptRoot '..\dist\NetConductor-win32-x64\*') -Destination $desktopRoot -Recurse -Force
$shell=New-Object -ComObject WScript.Shell
$link=$shell.CreateShortcut((Join-Path ([Environment]::GetFolderPath('Desktop')) 'Net Conductor.lnk'))
$link.TargetPath=Join-Path $desktopRoot 'NetConductor.exe'
$link.WorkingDirectory=$desktopRoot
$link.IconLocation=(Join-Path $desktopRoot 'NetConductor.exe')+',0'
$link.Save()
Write-Host 'Net Conductor installed. Open the desktop shortcut to join your server.'
