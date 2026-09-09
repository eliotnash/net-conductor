#Requires -RunAsAdministrator
$ErrorActionPreference='Stop'
if (Get-Service NetConductorAgent -ErrorAction SilentlyContinue) {
 Stop-Service NetConductorAgent
 sc.exe delete NetConductorAgent
}
Write-Host 'Background service removed. Existing WireGuard tunnels, proxy settings and backups are retained for review.'
