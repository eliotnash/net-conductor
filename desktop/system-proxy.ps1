param([ValidateSet('enable','restore','status')][string]$Action)
$ErrorActionPreference='Stop'
$key='HKCU:\Software\Microsoft\Windows\CurrentVersion\Internet Settings'
$backupPath=Join-Path $env:LOCALAPPDATA 'NetConductor\system-proxy-backup.json'
$names=@('ProxyEnable','ProxyServer','ProxyOverride')
if ($Action -eq 'status') {
 $current=Get-ItemProperty -LiteralPath $key
 @{enabled=($current.ProxyEnable -eq 1 -and $current.ProxyServer -eq '127.0.0.1:17891');backup=(Test-Path -LiteralPath $backupPath)} | ConvertTo-Json -Compress
 exit
}
if ($Action -eq 'enable') {
 if (-not (Test-Path -LiteralPath $backupPath)) {
  $current=Get-ItemProperty -LiteralPath $key
  $values=@{}
  foreach ($name in $names) { $values[$name]=@{exists=($null -ne $current.PSObject.Properties[$name]);value=$current.$name} }
  New-Item -ItemType Directory -Force (Split-Path $backupPath) | Out-Null
  $values | ConvertTo-Json -Depth 4 | Set-Content -LiteralPath $backupPath -Encoding UTF8
 }
 Set-ItemProperty -LiteralPath $key -Name ProxyEnable -Type DWord -Value 1
 Set-ItemProperty -LiteralPath $key -Name ProxyServer -Value '127.0.0.1:17891'
 $old=(Get-ItemProperty -LiteralPath $key).ProxyOverride
 $exceptions=(@($old -split ';')+@('<local>','127.*','10.77.*') | Where-Object {$_} | Select-Object -Unique) -join ';'
 Set-ItemProperty -LiteralPath $key -Name ProxyOverride -Value $exceptions
} else {
 if (-not (Test-Path -LiteralPath $backupPath)) { throw 'No saved proxy configuration to restore' }
 $saved=Get-Content -LiteralPath $backupPath -Raw | ConvertFrom-Json
 foreach ($name in $names) {
  if ($saved.$name.exists) { Set-ItemProperty -LiteralPath $key -Name $name -Value $saved.$name.value }
  else { Remove-ItemProperty -LiteralPath $key -Name $name -ErrorAction SilentlyContinue }
 }
 Remove-Item -LiteralPath $backupPath
}
Add-Type -TypeDefinition 'using System; using System.Runtime.InteropServices; public static class NetConductorInternet { [DllImport("wininet.dll", SetLastError=true)] public static extern bool InternetSetOption(IntPtr h, int o, IntPtr b, int l); }'
[NetConductorInternet]::InternetSetOption([IntPtr]::Zero,39,[IntPtr]::Zero,0) | Out-Null
[NetConductorInternet]::InternetSetOption([IntPtr]::Zero,37,[IntPtr]::Zero,0) | Out-Null
@{ok=$true} | ConvertTo-Json -Compress
