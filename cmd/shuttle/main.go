// main.go — Shuttle CLI entry point (Cobra)
// main.go — Shuttle CLI entry point (Cobra)
// main.go — Shuttle CLI 入口 (Cobra)
package main

import (
	"fmt"
	"os"
	"runtime"

	"strings"

	delta "github.com/henryborner/go-rsync"
	"github.com/henryborner/shuttle/internal/config"
	"github.com/henryborner/shuttle/internal/tui"
	"github.com/spf13/cobra"
)

var (
	cfgPath  string
	dryRun   bool
	verbose  bool
	workers  int
	algoName string

	versionStr = "0.1.5.0"
	rootCmd    = &cobra.Command{
		Use:   "shuttle",
		Short: "Shuttle — rsync-style delta sync for Windows",
		Long: `Shuttle is a Windows-native file sync tool.
Powered by a hand-optimized AVX2/SSE2/Go 3-tier checksum engine and rsync delta algorithm.
Config-file driven: define local→remote mappings in syncd.yaml, then push.`,
		Version: versionStr,
	}
)

func main() {
	// Double-click launch: start TUI directly (no terminal needed).
	// 双击启动：直接进入 TUI 界面。
	if len(os.Args) == 1 {
		runTUI(nil, nil)
		return
	}

	// push / 推送命令
	pushCmd := &cobra.Command{
		Use:   "push [task name]",
		Short: "Run sync tasks from config",
		Long:  "Run one or all sync tasks defined in syncd.yaml.",
		Run:   runPush,
	}
	pushCmd.Flags().StringVarP(&cfgPath, "config", "c", "syncd.yaml", "config file path")
	pushCmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview only, no changes")
	pushCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "detailed stats output")
	pushCmd.Flags().IntVarP(&workers, "workers", "w", 0, "parallel workers (0=config or 4)")
	pushCmd.Flags().StringVar(&algoName, "algo", "", "override checksum algorithm (md5/xxh64/sha256)")
	rootCmd.AddCommand(pushCmd)

	// tui / 终端界面
	rootCmd.AddCommand(&cobra.Command{
		Use:   "tui",
		Short: "Launch interactive terminal UI",
		Run:   runTUI,
	})

	// list / 列出任务
	rootCmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List all tasks and servers from config",
		Run:   runList,
	})

	// config / 配置信息
	rootCmd.AddCommand(&cobra.Command{
		Use:   "config",
		Short: "Show full config summary",
		Run:   runConfig,
	})

	// test / 测试连接
	testCmd := &cobra.Command{
		Use:   "test <server name>",
		Short: "Test SSH connection to a server",
		Args:  cobra.ExactArgs(1),
		Run:   runTest,
	}
	rootCmd.AddCommand(testCmd)

	// init / 生成配置
	rootCmd.AddCommand(&cobra.Command{
		Use:   "init",
		Short: "Generate sample syncd.yaml in current directory",
		Run:   runInit,
	})

	// version / 版本信息
	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Show version and build info",
		Run:   runVersion,
	})

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runVersion(cmd *cobra.Command, args []string) {
	fmt.Printf("Shuttle v%s — rsync-style delta sync for Windows\n", versionStr)
	fmt.Printf("  Go:     %s\n", runtime.Version())
	fmt.Printf("  OS:     %s\n", runtime.GOOS)
	fmt.Printf("  Arch:   %s\n", runtime.GOARCH)
	fmt.Printf("  Engine: AVX2/SSE2/Go 3-tier checksum\n")
	fmt.Printf("  Strong: %s\n", delta.GetDefault())
	fmt.Printf("  Algos:  %s\n", strings.Join(delta.ListAlgos(), ", "))
}

func runPush(cmd *cobra.Command, args []string) {
	taskName := ""
	if len(args) > 0 {
		taskName = args[0]
	}
	doSync(taskName, cfgPath, dryRun, verbose, workers, algoName)
}

func runConfig(cmd *cobra.Command, args []string) {
	cfg, err := config.Load("syncd.yaml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "No config found: %v\n", err)
		fmt.Println("Run 'shuttle init' to create one.")
		return
	}
	fmt.Printf("Config: syncd.yaml  (version %s)\n", cfg.Version)
	fmt.Printf("Language: %s  |  Checksum: %s  |  Workers: %d\n",
		cfg.Language, cfg.Checksum, cfg.Workers)
	fmt.Printf("Servers: %d  |  Tasks: %d\n", len(cfg.Servers), len(cfg.Tasks))
	fmt.Println()
	fmt.Println("── Servers ──")
	for _, s := range cfg.Servers {
		auth := "key"
		if s.Pass != "" {
			auth = "password"
		}
		fmt.Printf("  %-15s %s@%s:%d  (%s)\n", s.Name, s.User, s.Host, s.Port, auth)
	}
	fmt.Println()
	fmt.Println("── Tasks ──")
	for _, t := range cfg.Tasks {
		flags := ""
		if t.Options.Delete {
			flags += " delete"
		}
		if t.Options.Checksum {
			flags += " checksum"
		}
		if t.Options.Flat {
			flags += " flat"
		}
		if flags == "" {
			flags = " (defaults)"
		}
		fmt.Printf("  %-15s %s → %s%s\n", t.Name, t.Source, t.Target, flags)
	}
}

func runList(cmd *cobra.Command, args []string) {
	cfg, err := config.Load("syncd.yaml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "No config: %v\n", err)
		return
	}
	fmt.Println("Tasks:")
	for _, t := range cfg.Tasks {
		fmt.Printf("  %-15s %s\n", t.Name, t.Source)
	}
	fmt.Println()
	fmt.Println("Servers:")
	for _, s := range cfg.Servers {
		fmt.Printf("  %-15s %s@%s:%d\n", s.Name, s.User, s.Host, s.Port)
	}
}

func runTest(cmd *cobra.Command, args []string) {
	serverName := args[0]
	cfg, err := config.Load("syncd.yaml")
	if err != nil {
		fmt.Fprintf(os.Stderr, "No config: %v\n", err)
		os.Exit(1)
	}
	s := cfg.GetServer(serverName)
	if s == nil {
		fmt.Fprintf(os.Stderr, "Server not found: %s\n", serverName)
		os.Exit(1)
	}
	fmt.Printf("Testing %s@%s:%d ...\n", s.User, s.Host, s.Port)
	if err := testDial(s.Host, s.Port, s.User, s.KeyFile, s.Pass); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("OK — connected successfully")
}

func runInit(cmd *cobra.Command, args []string) {
	if _, err := os.Stat("syncd.yaml"); err == nil {
		fmt.Println("syncd.yaml already exists")
		return
	}
	os.WriteFile("syncd.yaml", []byte(initTemplate), 0644)
	fmt.Println("Created syncd.yaml — edit it and run 'shuttle push'")
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
