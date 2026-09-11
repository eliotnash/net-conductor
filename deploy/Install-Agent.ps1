#Requires -RunAsAdministrator
param([string]$Binary = (Join-Path $PSScriptRoot '..\dist\netconductor.exe'),[string]$DesktopUser = $env:USERNAME)
$ErrorActionPreference='Stop'
$installRoot=Join-Path $env:ProgramFiles 'NetConductor'
$dataRoot=Join-Path $env:ProgramData 'NetConductor'
New-Item -ItemType Directory -Force $installRoot,$dataRoot | Out-Null
if (Get-Service NetConductorAgent -ErrorAction SilentlyContinue) {
 $serviceProcessId=(Get-CimInstance Win32_Service -Filter "Name='NetConductorAgent'").ProcessId
 Stop-Service NetConductorAgent -ErrorAction Stop
 if ($serviceProcessId -gt 0) { Wait-Process -Id $serviceProcessId -Timeout 15 -ErrorAction SilentlyContinue }
}
for($attempt=0;$attempt -lt 10;$attempt++) {
 try {Copy-Item -LiteralPath $Binary -Destination (Join-Path $installRoot 'netconductor.exe') -Force;break}
 catch {if($attempt -eq 9){throw};Start-Sleep -Milliseconds 500}
}
$configPath=Join-Path $dataRoot 'agent.json'
if (-not (Test-Path -LiteralPath $configPath)) { & (Join-Path $installRoot 'netconductor.exe') -mode init-agent -config $configPath; if ($LASTEXITCODE -ne 0) { throw 'Configuration initialization failed' } }
$sid=(Get-LocalUser -Name $DesktopUser -ErrorAction Stop).SID.Value
icacls.exe $dataRoot /inheritance:r /grant:r '*S-1-5-18:(OI)(CI)F' '*S-1-5-32-544:(OI)(CI)F' "*${sid}:(OI)(CI)R" | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'Data ACL setup failed' }
if (-not (Get-Service NetConductorAgent -ErrorAction SilentlyContinue)) {
  $binPath='"'+(Join-Path $installRoot 'netconductor.exe')+'" -mode agent -config "'+$configPath+'"'
  New-Service -Name NetConductorAgent -DisplayName 'Net Conductor Agent' -BinaryPathName $binPath -StartupType Automatic | Out-Null
}
sc.exe failure NetConductorAgent reset= 86400 actions= restart/5000/restart/15000/restart/30000 | Out-Null
Start-Service NetConductorAgent
Get-Service NetConductorAgent | Select-Object Name,Status
