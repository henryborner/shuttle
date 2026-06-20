// main.go — Shuttle CLI 入口 (Cobra)
package main

import (
	"fmt"
	"os"

	"github.com/henryborner/shuttle/internal/config"
	"github.com/henryborner/shuttle/internal/tui"
	"github.com/spf13/cobra"
)

var (
	cfgPath string
	dryRun  bool
	rootCmd = &cobra.Command{
		Use:   "shuttle",
		Short: "🚀 Shuttle — 增量文件同步工具",
		Long: `Shuttle 是一个 Windows 原生的文件同步工具。
基于 rsync 增量算法，支持配置文件定义多组本地→远程映射。
通过 SFTP/SSH 将文件高效推送到服务器。`,
	}
)

func main() {
	pushCmd := &cobra.Command{
		Use:   "push [任务名]",
		Short: "执行同步任务",
		Long:  "执行配置文件中的同步任务。不指定任务名则执行全部。",
		Run:   runPush,
	}
	pushCmd.Flags().StringVarP(&cfgPath, "config", "c", "syncd.yaml", "配置文件路径")
	pushCmd.Flags().BoolVar(&dryRun, "dry-run", false, "模拟运行，不实际同步")
	rootCmd.AddCommand(pushCmd)

	// TUI 命令
	rootCmd.AddCommand(&cobra.Command{
		Use:   "tui",
		Short: "Launch interactive TUI",
		Long:  "Launch the interactive terminal UI for managing mappings, servers, and sync.",
		Run:   runTUI,
	})

	rootCmd.AddCommand(&cobra.Command{
		Use:   "config",
		Short: "显示配置文件信息和位置",
		Run:   runConfig,
	})

	rootCmd.AddCommand(&cobra.Command{
		Use:   "init",
		Short: "在当前目录生成示例配置文件",
		Run:   runInit,
	})

	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "显示版本信息",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("Shuttle v0.1.1 - rsync-style delta sync for Windows")
			fmt.Println("增量: Adler-32 + MD5/SHA256 | 传输: SFTP")
		},
	})

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runPush(cmd *cobra.Command, args []string) {
	taskName := ""
	if len(args) > 0 {
		taskName = args[0]
	}
	doSync(taskName, cfgPath, dryRun)
}

func runConfig(cmd *cobra.Command, args []string) {
	cwd, _ := os.Getwd()
	fmt.Printf("📁 当前目录: %s\n", cwd)
	fmt.Printf("📄 默认配置: syncd.yaml\n")
	if _, err := os.Stat("syncd.yaml"); err == nil {
		fmt.Println("✅ 找到 syncd.yaml")
	} else {
		fmt.Println("⚠️  未找到 — 运行 'shuttle init' 生成示例")
	}
}

func runInit(cmd *cobra.Command, args []string) {
	example := `# Shuttle 同步配置文件
version: "1.0"

servers:
  - name: myserver
    host: 192.168.1.100
    port: 22
    user: deploy
    key_file: ~/.ssh/id_ed25519

tasks:
  - name: web
    source: E:\projects\website\dist\
    target: myserver:/var/www/html/
    options:
      delete: true
      exclude:
        - "*.tmp"
        - ".DS_Store"
      checksum: false
`
	if _, err := os.Stat("syncd.yaml"); err == nil {
		fmt.Println("⚠️  syncd.yaml 已存在")
		return
	}
	os.WriteFile("syncd.yaml", []byte(example), 0644)
	fmt.Println("✅ 已生成 syncd.yaml")
}

func runTUI(cmd *cobra.Command, args []string) {
	cfg, err := config.Load("syncd.yaml")
	if err != nil {
		// 配置文件不存在时允许空配置进入 TUI
		cfg = &config.Config{Version: "1.0"}
	}

	if err := tui.Run(cfg, "syncd.yaml"); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		os.Exit(1)
	}
}
