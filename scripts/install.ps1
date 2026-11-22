$ErrorActionPreference = "Stop"

$OwnerRepo = if ($env:PB_REPO) { $env:PB_REPO } else { "yesabhishek/pastebin-cli" }
$BinDir = if ($env:PB_BIN_DIR) { $env:PB_BIN_DIR } else { Join-Path $HOME ".local\bin" }
$Name = "pb.exe"

$arch = switch ($env:PROCESSOR_ARCHITECTURE.ToLower()) {
    "amd64" { "amd64" }
    "arm64" { "arm64" }
    default { throw "unsupported architecture: $env:PROCESSOR_ARCHITECTURE" }
}

$apiUrl = "https://api.github.com/repos/$OwnerRepo/releases/latest"
$release = Invoke-RestMethod -Uri $apiUrl
if (-not $release.tag_name) {
    throw "failed to resolve latest release tag from $apiUrl"
}

$asset = "pb_windows_$arch.zip"
$url = "https://github.com/$OwnerRepo/releases/download/$($release.tag_name)/$asset"

New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
$tmp = Join-Path ([System.IO.Path]::GetTempPath()) ("pb-install-" + [Guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Force -Path $tmp | Out-Null

try {
    $zipPath = Join-Path $tmp $asset
    Invoke-WebRequest -Uri $url -OutFile $zipPath
    $sumsPath = Join-Path $tmp "SHA256SUMS"
    Invoke-WebRequest -Uri "https://github.com/$OwnerRepo/releases/download/$($release.tag_name)/SHA256SUMS" -OutFile $sumsPath
    $expected = (Get-Content $sumsPath | Where-Object { ($_ -split '\s+')[1] -eq $asset } | ForEach-Object { ($_ -split '\s+')[0] }) | Select-Object -First 1
    $actual = (Get-FileHash -Algorithm SHA256 -Path $zipPath).Hash.ToLower()
    if (-not $expected -or $expected.ToLower() -ne $actual) {
        throw "checksum verification failed for $asset; refusing to install"
    }
    Expand-Archive -Path $zipPath -DestinationPath $tmp -Force
    Copy-Item -Path (Join-Path $tmp $Name) -Destination (Join-Path $BinDir $Name) -Force
    Write-Host "Installed $Name to $(Join-Path $BinDir $Name)"
    Write-Host "Add $BinDir to your PATH if needed."
}
finally {
    Remove-Item -Recurse -Force $tmp
}
