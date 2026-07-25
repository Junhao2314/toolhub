param(
  [Parameter(Mandatory=$true)][string]$BinaryPath,
  [string]$ServiceName = "ToolHubAgent"
)

$resolved = (Resolve-Path -LiteralPath $BinaryPath).Path
if (-not $resolved.EndsWith(".exe")) { throw "BinaryPath must point to toolhub-agent.exe" }

sc.exe create $ServiceName binPath= "`"$resolved`" run" start= auto DisplayName= "ToolHub Agent"
if ($LASTEXITCODE -ne 0) { throw "Failed to create Windows service" }
sc.exe description $ServiceName "ToolHub node inventory and signed task agent"
sc.exe failure $ServiceName reset= 86400 actions= restart/5000/restart/15000/restart/60000
sc.exe start $ServiceName
