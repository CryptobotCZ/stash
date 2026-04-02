powershell -ExecutionPolicy Bypass -File scripts/build-docker-armv7-local.ps1 -SkipPull
docker save -o stash_local.tar stashapp/stash:armv7-local 

