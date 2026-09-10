#Requires -RunAsAdministrator
param([string]$DesktopUser=$env:USERNAME)
$ErrorActionPreference='Stop'
if (Get-Service NetConductorAgent -ErrorAction SilentlyContinue) {
 Stop-Service NetConductorAgent
 sc.exe delete NetConductorAgent
}
$desktopSid=(Get-LocalUser -Name $DesktopUser -ErrorAction Stop).SID.Value
$profilePath=(Get-ItemProperty -LiteralPath ('HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList\'+$desktopSid) -ErrorAction Stop).ProfileImagePath
$startupLink=Join-Path ([Environment]::ExpandEnvironmentVariables($profilePath)) 'AppData\Roaming\Microsoft\Windows\Start Menu\Programs\Startup\Net Conductor.lnk'
if (Test-Path -LiteralPath $startupLink) { Remove-Item -LiteralPath $startupLink }
Write-Host 'Background service removed. Existing WireGuard tunnels, proxy settings and backups are retained for review.'
