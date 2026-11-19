# Abre o painel no Windows rodando o binário Linux dentro do WSL.
#   .\start-design.ps1                          # config.json, distro padrão do WSL
#   .\start-design.ps1 -Distro Ubuntu-24.04 -Config config.wsl.json
#   .\start-design.ps1 -Preview                 # dados de exemplo, porta 7788
param(
    [switch]$Preview,
    [string]$Distro = '',
    [string]$Config = 'config.json'
)
$ErrorActionPreference = 'Stop'
if ($Preview) {
    & (Join-Path $PSScriptRoot 'wt-street.exe') -demo
    exit $LASTEXITCODE
}
$wsl = @()
if ($Distro) { $wsl = @('-d', $Distro) }
$linuxDir = (& wsl.exe @wsl -- wslpath -u $PSScriptRoot).Trim()
if ($LASTEXITCODE -ne 0) { throw 'Não foi possível localizar a pasta no WSL.' }
Write-Host 'Abra http://127.0.0.1:7777/ no navegador. Ctrl+C encerra.'
& wsl.exe @wsl -- "$linuxDir/wt-street-linux" -config "$linuxDir/$Config" -no-open
exit $LASTEXITCODE
