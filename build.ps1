# $env:PATH =
$env:PATH += ";C:\bin\mingw\mingw64\bin\"
$env:PATH += ";C:\bin\mingw\mingw32\bin\"
#cho $env:PATH;
#exit;
$env:CGO_ENABLED=1;
& "C:\bin\mingw\mingw64\bin\mingw32-make.exe" pre-ui
& "C:\bin\mingw\mingw64\bin\mingw32-make.exe" generate
& "C:\bin\mingw\mingw64\bin\mingw32-make.exe" ui
& "C:\bin\mingw\mingw64\bin\mingw32-make.exe" stash
& "C:\bin\mingw\mingw64\bin\mingw32-make.exe" build-release
