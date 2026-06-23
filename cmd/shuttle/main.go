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
		Short: "Shuttle — rsync-style delta sync tool",
		Long: `Shuttle is a Windows-native file sync tool.
Uses rsync delta algorithm with config-file defined local→remote mappings.
Transfers files efficiently over SFTP/SSH.`,
	}
)

func main() {
	pushCmd := &cobra.Command{
		Use:   "push [task name]",
		Short: "Run sync tasks",
		Long:  "Run sync tasks from config. Without task name, runs all.",
		Run:   runPush,
	}
	pushCmd.Flags().StringVarP(&cfgPath, "config", "c", "syncd.yaml", "config file path")
	pushCmd.Flags().BoolVar(&dryRun, "dry-run", false, "dry run without actual sync")
	rootCmd.AddCommand(pushCmd)

	rootCmd.AddCommand(&cobra.Command{
		Use:   "tui",
		Short: "Launch interactive TUI",
		Run:   runTUI,
	})

	rootCmd.AddCommand(&cobra.Command{
		Use:   "config",
		Short: "Show config file info and location",
		Run:   runConfig,
	})

	rootCmd.AddCommand(&cobra.Command{
		Use:   "init",
		Short: "Generate sample config in current directory",
		Run:   runInit,
	})

	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Show version info",
		Run: func(cmd *cobra.Command, args []string) {
			fmt.Println("Shuttle v0.1.2.9 - rsync-style delta sync for Windows")
			fmt.Println("Delta: Adler-32 + MD5/SHA256 | Transport: SFTP")
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
	fmt.Printf("CWD: %s\n", cwd)
	fmt.Printf("Config: syncd.yaml\n")
	if _, err := os.Stat("syncd.yaml"); err == nil {
		fmt.Println("Found syncd.yaml")
		fmt.Println("Found syncd.yaml")
	} else {
		fmt.Println("Not found — run 'shuttle init' to create")
	}
}

func runInit(cmd *cobra.Command, args []string) {
	if _, err := os.Stat("syncd.yaml"); err == nil {
		fmt.Println("syncd.yaml already exists")
		return
	}
	os.WriteFile("syncd.yaml", []byte(initTemplate), 0644)
	fmt.Println("Created syncd.yaml")
}

const initTemplate = `# Shuttle 同步配置文件
# 用法: shuttle push [任务名]

version: "1.0"
language: zh               # en / zh
checksum: xxh64            # md5 / sha256 / xxh64
workers: 4                 # delta 并行数: 1=串行 2/4/8=并行

servers:
  - name: myserver
    host: 192.168.1.100
    port: 22
    user: deploy
    key_file: ~/.ssh/id_ed25519
    protect:                # 保护列表：匹配的远端文件绝不覆盖/删除
      - "*.db"
      - "*.pem"
      - ".env"

tasks:
  - name: web
    source: E:\projects\website\dist\
    target: myserver:/var/www/html/
    options:
      delete: true           # 删除远程多余文件
      exclude:
        - "*.tmp"
        - ".DS_Store"
      checksum: false        # true: 用校验和对比; false: 用时间+大小
      flat: false            # true: 不套源文件夹名
`

func runTUI(cmd *cobra.Command, args []string) {
	cfg, err := config.Load("syncd.yaml")
	if err != nil {
		if os.IsNotExist(err) {
			// First launch: generate default config then enter TUI
			os.WriteFile("syncd.yaml", []byte(initTemplate), 0644)
			fmt.Println("Created syncd.yaml — editing in TUI...")
			cfg, _ = config.Load("syncd.yaml")
		} else {
			fmt.Fprintf(os.Stderr, "Config load failed: %v\n", err)
			os.Exit(1)
		}
	}

	if err := tui.Run(cfg, "syncd.yaml"); err != nil {
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		os.Exit(1)
	}
}
