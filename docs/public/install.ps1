#Requires -Version 5.1

$ErrorActionPreference = "Stop"

$identity = [Security.Principal.WindowsIdentity]::GetCurrent()
$principal = [Security.Principal.WindowsPrincipal]::new($identity)
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw "Run this installer from an Administrator PowerShell."
}

$repository = "sirrobot01/bifrost"
$download = if ($env:BIFROST_DOWNLOAD) { $env:BIFROST_DOWNLOAD.TrimEnd("/") } else { "https://github.com/$repository/releases/latest/download" }
$archiveArch = switch ([Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString()) {
    "X64" { "x86_64" }
    "Arm64" { "aarch64" }
    default { throw "Unsupported Windows architecture $([Runtime.InteropServices.RuntimeInformation]::OSArchitecture)." }
}

$workDirectory = Join-Path ([IO.Path]::GetTempPath()) ("bifrost-" + [guid]::NewGuid())
$installDirectory = Join-Path $env:ProgramFiles "Bifrost"
$configDirectory = Join-Path $env:ProgramData "Bifrost"
$executable = Join-Path $installDirectory "bifrost.exe"
$checksums = Join-Path $workDirectory "checksums.txt"

New-Item -ItemType Directory -Force -Path $workDirectory | Out-Null
$running = @()
try {
    Invoke-WebRequest -UseBasicParsing -Uri "$download/checksums.txt" -OutFile $checksums
    $pattern = "^([0-9a-f]{64})\s+(bifrost_.+_windows_$([regex]::Escape($archiveArch))\.tar\.gz)$"
    $match = Get-Content $checksums | ForEach-Object {
        if ($_ -match $pattern) { [pscustomobject]@{ Hash = $Matches[1]; Name = $Matches[2] } }
    } | Select-Object -First 1
    if (-not $match) {
        throw "The latest release has no Windows $archiveArch archive."
    }

    $archive = Join-Path $workDirectory $match.Name
    Write-Host "Downloading $($match.Name)"
    Invoke-WebRequest -UseBasicParsing -Uri "$download/$($match.Name)" -OutFile $archive
    $actualHash = (Get-FileHash -Algorithm SHA256 $archive).Hash.ToLowerInvariant()
    if ($actualHash -ne $match.Hash) {
        throw "Checksum verification failed for $($match.Name)."
    }
    & tar.exe -xzf $archive -C $workDirectory bifrost.exe
    if ($LASTEXITCODE -ne 0) {
        throw "Could not extract $($match.Name)."
    }

    foreach ($name in @("bifrost", "bifrost-edge")) {
        $service = Get-Service -Name $name -ErrorAction SilentlyContinue
        if ($service -and $service.Status -ne "Stopped") {
            $running += $name
            Stop-Service -Name $name -Force
            $service.WaitForStatus("Stopped", [TimeSpan]::FromMinutes(3))
        }
    }

    New-Item -ItemType Directory -Force -Path $installDirectory, $configDirectory | Out-Null
    Copy-Item -Force (Join-Path $workDirectory "bifrost.exe") $executable

    $acl = [Security.AccessControl.DirectorySecurity]::new()
    $acl.SetAccessRuleProtection($true, $false)
    $inheritance = [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit
    $propagation = [Security.AccessControl.PropagationFlags]::None
    foreach ($sid in @("S-1-5-18", "S-1-5-32-544")) {
        $account = [Security.Principal.SecurityIdentifier]::new($sid)
        $rule = [Security.AccessControl.FileSystemAccessRule]::new($account, "FullControl", $inheritance, $propagation, "Allow")
        $acl.AddAccessRule($rule)
    }
    Set-Acl -Path $configDirectory -AclObject $acl

    $serviceDefinitions = @(
        @{ Name = "bifrost"; DisplayName = "Bifrost"; Description = "Bifrost IPv6-native ingress"; Arguments = "serve --config `"$(Join-Path $configDirectory 'config.yaml')`"" },
        @{ Name = "bifrost-edge"; DisplayName = "Bifrost Edge"; Description = "Bifrost IPv4 edge"; Arguments = "edge --config `"$(Join-Path $configDirectory 'edge.yaml')`"" }
    )
    foreach ($definition in $serviceDefinitions) {
        $binaryPath = "`"$executable`" $($definition.Arguments)"
        if (Get-Service -Name $definition.Name -ErrorAction SilentlyContinue) {
            & sc.exe config $definition.Name "binPath= $binaryPath" | Out-Null
            if ($LASTEXITCODE -ne 0) { throw "Could not update service $($definition.Name)." }
        } else {
            New-Service -Name $definition.Name -BinaryPathName $binaryPath -DisplayName $definition.DisplayName -Description $definition.Description -StartupType Manual | Out-Null
        }
        if (-not [Diagnostics.EventLog]::SourceExists($definition.Name)) {
            New-EventLog -LogName Application -Source $definition.Name
        }
    }

    $machinePath = [Environment]::GetEnvironmentVariable("Path", "Machine")
    if (($machinePath -split ";") -notcontains $installDirectory) {
        [Environment]::SetEnvironmentVariable("Path", ($machinePath.TrimEnd(";") + ";" + $installDirectory), "Machine")
    }
    $env:Path = $installDirectory + ";" + $env:Path

    foreach ($name in $running) { Start-Service -Name $name }
    $running = @()
    Write-Host "Installed $executable and the Windows services without enabling new services."
    Write-Host "Next: bifrost doctor"
}
finally {
    foreach ($name in $running) { Start-Service -Name $name -ErrorAction SilentlyContinue }
    Remove-Item -Recurse -Force -ErrorAction SilentlyContinue $workDirectory
}
