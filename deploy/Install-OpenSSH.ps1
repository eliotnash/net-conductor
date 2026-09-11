#Requires -RunAsAdministrator
param([string]$DesktopUser=$env:USERNAME,[string]$PrivateNetwork='10.77.0.0/24')
$ErrorActionPreference='Stop'
$sshRoot=Join-Path $env:WINDIR 'System32\OpenSSH'
$existingSSH=(Test-Path (Join-Path $sshRoot 'sshd.exe')) -and (Get-Service sshd -ErrorAction SilentlyContinue)
$cap=Get-WindowsCapability -Online -Name 'OpenSSH.Server~~~~0.0.1.0'
if ($cap.State -ne 'Installed' -and -not $existingSSH) {
 $result=Add-WindowsCapability -Online -Name 'OpenSSH.Server~~~~0.0.1.0'
 if ($result.RestartNeeded) {throw 'OpenSSH installation requires a Windows restart. Restart manually before retrying.'}
}
$sshRoot=Join-Path $env:WINDIR 'System32\OpenSSH'
Set-Service sshd -StartupType Automatic
& (Join-Path $sshRoot 'ssh-keygen.exe') -A
if ($LASTEXITCODE -ne 0) {throw 'SSH host key initialization failed'}
Start-Service sshd
& (Join-Path $sshRoot 'sshd.exe') -t
if ($LASTEXITCODE -ne 0) {throw 'Existing sshd configuration is invalid'}
$user=Get-LocalUser -Name $DesktopUser -ErrorAction Stop
$profile=(Get-ItemProperty -LiteralPath ('HKLM:\SOFTWARE\Microsoft\Windows NT\CurrentVersion\ProfileList\'+$user.SID.Value)).ProfileImagePath
$effective=& (Join-Path $sshRoot 'sshd.exe') -T -C ('user='+$DesktopUser+',host=localhost,addr=127.0.0.1')
$keySetting=($effective | Where-Object {$_ -like 'authorizedkeysfile *'}) -replace '^authorizedkeysfile\s+',''
$candidate=($keySetting -split '\s+')[0]
if ($candidate -eq '__PROGRAMDATA__/ssh/administrators_authorized_keys') {$authorized=Join-Path $env:ProgramData 'ssh\administrators_authorized_keys'}
elseif ($candidate -eq '.ssh/authorized_keys' -or $candidate -eq '.ssh\authorized_keys') {$authorized=Join-Path $profile '.ssh\authorized_keys'}
else {throw 'Custom AuthorizedKeysFile requires manual review; existing SSH configuration has been preserved.'}
$dataRoot=Join-Path $env:ProgramData 'NetConductor'
$configPath=Join-Path $dataRoot 'agent.json'
$cfg=Get-Content -LiteralPath $configPath -Raw -Encoding UTF8 | ConvertFrom-Json
# Resolve the effective bind without replacing an existing sshd_config.
$portLine=($effective | Where-Object {$_ -like 'port *'} | Select-Object -First 1)
$port=[int](($portLine -split '\s+')[1])
if ($port -lt 1 -or $port -gt 65535) {throw 'Invalid effective SSH port'}
$sshAddress=$null
foreach ($hostAddress in @('127.0.0.1',$cfg.ip,'::1')) {
 if (-not $hostAddress) {continue}
 $found=Get-NetTCPConnection -State Listen -LocalPort $port -ErrorAction SilentlyContinue | Where-Object {$_.LocalAddress -eq $hostAddress -or ($hostAddress -eq '127.0.0.1' -and $_.LocalAddress -eq '0.0.0.0') -or ($hostAddress -eq '::1' -and $_.LocalAddress -eq '::')}
 if ($found) {$sshAddress=if($hostAddress -like '*:*') {'['+$hostAddress+']:'+$port} else {$hostAddress+':'+$port}; break}
}
if (-not $sshAddress) {throw 'SSH is not listening on loopback or this device tunnel IP; review ListenAddress without replacing sshd_config.'}
$cfg | Add-Member -NotePropertyName sshAddress -NotePropertyValue $sshAddress -Force
$cfg | Add-Member -NotePropertyName sshUser -NotePropertyValue $DesktopUser -Force
$cfg | Add-Member -NotePropertyName sshKey -NotePropertyValue (Join-Path $dataRoot 'ssh_local_ed25519') -Force
$cfg | Add-Member -NotePropertyName sshAuthorizedKeys -NotePropertyValue $authorized -Force
$hostPrint=(& (Join-Path $sshRoot 'ssh-keygen.exe') -lf (Join-Path $env:ProgramData 'ssh\ssh_host_ed25519_key.pub') -E sha256) -split '\s+'
if ($LASTEXITCODE -ne 0 -or $hostPrint[1] -notlike 'SHA256:*') {throw 'SSH host fingerprint could not be verified'}
$cfg | Add-Member -NotePropertyName sshHostFingerprint -NotePropertyValue $hostPrint[1] -Force
[IO.File]::WriteAllText($configPath,($cfg | ConvertTo-Json -Depth 20),[Text.UTF8Encoding]::new($false))
New-Item -ItemType Directory -Force -Path (Split-Path $authorized) | Out-Null
& (Join-Path $env:ProgramFiles 'NetConductor\netconductor.exe') -mode init-local-ssh -config $configPath
if ($LASTEXITCODE -ne 0) {throw 'Local SSH key authorization failed'}
if ($authorized -like '*administrators_authorized_keys') {
 icacls.exe $authorized /inheritance:r /grant:r '*S-1-5-18:F' '*S-1-5-32-544:F' | Out-Null
} else {
 icacls.exe $authorized /inheritance:r /grant:r '*S-1-5-18:F' '*S-1-5-32-544:F' ('*'+$user.SID.Value+':F') | Out-Null
}
icacls.exe $cfg.sshKey /inheritance:r /grant:r '*S-1-5-18:F' '*S-1-5-32-544:F' | Out-Null
if ($LASTEXITCODE -ne 0) {throw 'SSH key ACL setup failed'}
$portLine=($effective | Where-Object {$_ -like 'port *'} | Select-Object -First 1)
$port=[int](($portLine -split '\s+')[1])
# Custom ports use the validated effective listener in sshAddress.
Get-NetFirewallRule -Name 'OpenSSH-Server-In-TCP' -ErrorAction SilentlyContinue | Set-NetFirewallRule -RemoteAddress $PrivateNetwork
if (Get-NetFirewallRule -Name 'NetConductor-SSH' -ErrorAction SilentlyContinue) {Set-NetFirewallRule -Name 'NetConductor-SSH' -RemoteAddress $PrivateNetwork; Get-NetFirewallRule -Name 'NetConductor-SSH' | Get-NetFirewallPortFilter | Set-NetFirewallPortFilter -LocalPort $port}
else {New-NetFirewallRule -Name 'NetConductor-SSH' -DisplayName 'Net Conductor SSH (private network)' -Direction Inbound -Action Allow -Protocol TCP -LocalPort $port -RemoteAddress $PrivateNetwork | Out-Null}
Restart-Service NetConductorAgent
Write-Host 'OpenSSH ready; existing authorized keys preserved, server key will be synchronized after joining.'
