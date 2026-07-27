This directory contains pre-compiled shuttle_agent binaries for each target platform.
They are embedded into shuttle.exe via go:embed.

Build them with:
  GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o linux_amd64 ..\..\..\cmd\shuttle_agent\
  GOOS=linux GOARCH=arm64 go build -ldflags="-s -w" -o linux_arm64 ..\..\..\cmd\shuttle_agent\

Or use the build script:  build_agents.ps1
