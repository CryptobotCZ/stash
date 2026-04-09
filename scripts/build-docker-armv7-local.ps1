param(
    [string]$CompilerImage = "stashapp/compiler:12",
    [string]$ContainerName = "stash-build-armv7-local",
    [string]$ImageTag = "stashapp/stash:armv7-local",
    [string]$DockerContext = "",
    [switch]$SkipPull
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

if (-not [string]::IsNullOrWhiteSpace($DockerContext)) {
    $env:DOCKER_CONTEXT = $DockerContext
}

function Invoke-Step {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Message,
        [Parameter(Mandatory = $true)]
        [scriptblock]$Action
    )

    Write-Host "==> $Message"
    & $Action
}

function Test-DockerContainerExists {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Name
    )

    $result = docker ps -aq --filter "name=^${Name}$"
    if ($LASTEXITCODE -ne 0) {
        throw "Failed to query docker containers."
    }

    return -not [string]::IsNullOrWhiteSpace(($result | Out-String).Trim())
}

function Remove-DockerContainerIfExists {
    param(
        [Parameter(Mandatory = $true)]
        [string]$Name
    )

    if (Test-DockerContainerExists -Name $Name) {
        Invoke-Step -Message "Removing existing container '$Name'" -Action {
            docker rm -f -v $Name | Out-Host
            if ($LASTEXITCODE -ne 0) {
                throw "Failed to remove container '$Name'."
            }
        }
    }
}

$repoRoot = Split-Path -Parent $PSScriptRoot
$distDir = Join-Path $repoRoot "dist"
$goCacheDir = Join-Path $repoRoot ".go-cache"
$goModCacheDir = Join-Path $repoRoot ".go-mod-cache"
$pnpmStoreDir = Join-Path $repoRoot ".pnpm-store"

Push-Location $repoRoot
try {
    Invoke-Step -Message "Checking Docker availability" -Action {
        docker version | Out-Host
        if ($LASTEXITCODE -ne 0) {
            throw "Docker is not available."
        }
    }

    if (-not $SkipPull) {
        Invoke-Step -Message "Pulling compiler image $CompilerImage" -Action {
            docker pull $CompilerImage | Out-Host
            if ($LASTEXITCODE -ne 0) {
                throw "Failed to pull compiler image '$CompilerImage'."
            }
        }
    }

    Invoke-Step -Message "Ensuring local cache directories exist" -Action {
        $null = New-Item -ItemType Directory -Force -Path $goCacheDir
        $null = New-Item -ItemType Directory -Force -Path $goModCacheDir
        $null = New-Item -ItemType Directory -Force -Path $pnpmStoreDir
    }

    Remove-DockerContainerIfExists -Name $ContainerName

    Invoke-Step -Message "Starting compiler container $ContainerName" -Action {
        docker run -d --name $ContainerName `
            --mount "type=bind,source=${repoRoot},target=/stash" `
            --mount "type=bind,source=${goCacheDir},target=/root/.cache/go-build" `
            --mount "type=bind,source=${goModCacheDir},target=/go/pkg/mod" `
            --mount "type=bind,source=${pnpmStoreDir},target=/root/.pnpm-store" `
            -w /stash $CompilerImage tail -f /dev/null | Out-Host
        if ($LASTEXITCODE -ne 0) {
            throw "Failed to start compiler container '$ContainerName'."
        }
    }

    try {
        Invoke-Step -Message "Installing UI dependencies in compiler container" -Action {
            docker exec -t $ContainerName /bin/bash -c "make CI=1 pre-ui" | Out-Host
            if ($LASTEXITCODE -ne 0) {
                throw "make pre-ui failed."
            }
        }

        Invoke-Step -Message "Generating code in compiler container" -Action {
            docker exec -t $ContainerName /bin/bash -c "make generate" | Out-Host
            if ($LASTEXITCODE -ne 0) {
                throw "make generate failed."
            }
        }

        Invoke-Step -Message "Building UI in compiler container" -Action {
            docker exec -t $ContainerName /bin/bash -c "make ui" | Out-Host
            if ($LASTEXITCODE -ne 0) {
                throw "make ui failed."
            }
        }

        Invoke-Step -Message "Cross-compiling ARMv7 binary in compiler container" -Action {
            docker exec -t $ContainerName /bin/bash -c "make build-cc-linux-arm32v7" | Out-Host
            if ($LASTEXITCODE -ne 0) {
                throw "make build-cc-linux-arm32v7 failed."
            }
        }
    }
    finally {
        Remove-DockerContainerIfExists -Name $ContainerName
    }

    if (-not (Test-Path (Join-Path $distDir "stash-linux-arm32v7"))) {
        throw "Expected artifact '$distDir\stash-linux-arm32v7' was not produced."
    }

    Invoke-Step -Message "Building local ARMv7 Docker image $ImageTag" -Action {
        docker buildx build --platform linux/arm/v7 --tag $ImageTag --load -f docker/ci/x86_64/Dockerfile dist/ | Out-Host
        if ($LASTEXITCODE -ne 0) {
            throw "docker buildx build failed."
        }
    }

    Invoke-Step -Message "Inspecting built image" -Action {
        docker image inspect $ImageTag --format "{{.Os}}/{{.Architecture}}/{{.Variant}}" | Out-Host
        if ($LASTEXITCODE -ne 0) {
            throw "docker image inspect failed."
        }
    }
}
finally {
    Pop-Location
}
