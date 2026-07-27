// embed.go — embed pre-compiled agent binaries for all target platforms.
// The shuttle.exe ships with agents for linux/amd64 and linux/arm64,
// auto-detecting the remote architecture on deploy.
// embed.go — 内嵌各平台的预编译 agent 二进制。
// shuttle.exe 携带 linux/amd64 和 linux/arm64 的 agent，部署时自动检测远端架构。

package agent

import "embed"

//go:embed agents/*
var agentsFS embed.FS

// archMap maps uname -m output to GOARCH + embedded file name.
// archMap 将 uname -m 输出映射到 GOARCH 和内嵌文件名。
var archMap = map[string]string{
	"x86_64":  "amd64",
	"amd64":   "amd64",
	"aarch64": "arm64",
	"arm64":   "arm64",
	"armv8l":  "arm64",
}

// agentFileName returns the embedded file path for a given GOARCH.
// agentFileName 返回给定 GOARCH 的内嵌文件路径。
func agentFileName(goarch string) string {
	return "agents/linux_" + goarch
}
