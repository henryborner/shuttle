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
	cfgPath    string
	dryRun     bool
	verbose    bool
	workers    int
	algoName   string
	schemaFlag bool

	versionStr = "0.1.5.4"
	rootCmd    = &cobra.Command{
		Use:   "shuttle",
		Short: "Incremental file sync over SSH",
		Long: `Shuttle syncs local directories to remote Linux servers over SSH.

It compares source and target using the rsync delta algorithm:
files that exist on both sides transfer only a checksum signature
(a few KB) instead of the full file content. Only changed portions
of files are sent across the network.

Mappings between local paths and remote servers are defined in a
syncd.yaml config file. A terminal UI (TUI) is also available for
interactive management.

Getting started:
  shuttle init                 create a config template
  shuttle config --schema      full field reference with examples
  shuttle push                 run all sync tasks`,
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

	// push
	pushCmd := &cobra.Command{
		Use:   "push [task name]",
		Short: "Execute one or all sync tasks",
		Long: `Run sync tasks defined in syncd.yaml.

If a task name is given, only that task runs. Otherwise all tasks
are executed in order. Each task connects to its target server via
SSH, compares local and remote files, and transfers only the
differences (delta).

Quick reference:
  Folder sync:  source ends with \ or /  →  contents mapped to target/
  Single file:  source has no trailing slash  →  file placed at target path
  Target /:     destination is a directory to map into
  Target no /:  exact file path (single-file tasks)

See 'shuttle config --schema' for the complete field reference.`,
		Run: runPush,
	}
	pushCmd.Flags().StringVarP(&cfgPath, "config", "c", "syncd.yaml", "path to YAML config file")
	pushCmd.Flags().BoolVar(&dryRun, "dry-run", false, "show what would be transferred without making changes")
	pushCmd.Flags().BoolVarP(&verbose, "verbose", "v", false, "print per-file transfer details and wire bytes sent")
	pushCmd.Flags().IntVarP(&workers, "workers", "w", 0, "parallel delta workers (0 uses config default, 1=serial, max 8)")
	pushCmd.Flags().StringVar(&algoName, "algo", "", "checksum algorithm: md5, sha256, xxh64, or xxh3 (overrides config)")
	rootCmd.AddCommand(pushCmd)

	// tui
	rootCmd.AddCommand(&cobra.Command{
		Use:   "tui",
		Short: "Open the terminal UI",
		Long: `Launch the interactive terminal user interface.

The TUI provides panes for dashboard (sync status overview),
mapping management (add/edit/delete sync tasks), server management
(test connection, deploy agent), a file explorer, and settings
(language, checksum algorithm, worker count).`,
		Run: runTUI,
	})

	// list
	rootCmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "Print all tasks and servers from syncd.yaml",
		Long:  `Read syncd.yaml and print every configured task and server to stdout.`,
		Run:   runList,
	})

	// config
	configCmd := &cobra.Command{
		Use:   "config [--schema]",
		Short: "Print the syncd.yaml config summary or field reference",
		Long: `Load syncd.yaml and display a structured summary:
servers (name, host, port, user, auth method) and tasks
(name, source, target, enabled options).

With --schema: print a complete field reference including type
info, descriptions, examples (folder + single file), and the
list of available checksum algorithms.`,
		Run: runConfig,
	}
	configCmd.Flags().BoolVar(&schemaFlag, "schema", false, "print full config field reference with examples")
	rootCmd.AddCommand(configCmd)

	// test
	testCmd := &cobra.Command{
		Use:   "test <server name>",
		Short: "Verify SSH connectivity to a server",
		Long: `Open an SSH connection to the named server and report success or failure.

This is useful before running sync tasks to ensure the server
is reachable and the key or password is accepted.`,
		Args: cobra.ExactArgs(1),
		Run:  runTest,
	}
	rootCmd.AddCommand(testCmd)

	// init
	rootCmd.AddCommand(&cobra.Command{
		Use:   "init",
		Short: "Write a syncd.yaml template to the current directory",
		Long: `Create a new syncd.yaml file with commented examples for
both folder syncs (website deployment) and single-file syncs
(config push). Safe to run — will not overwrite an existing file.

Next steps:
  Edit syncd.yaml to set your server and task
  shuttle config --schema   view all available fields
  shuttle test <server>     verify SSH connectivity
  shuttle push --dry-run    preview what will be transferred`,
		Run: runInit,
	})

	// version
	rootCmd.AddCommand(&cobra.Command{
		Use:   "version",
		Short: "Print version, Go runtime, and available checksum algorithms",
		Long:  `Display the Shuttle version, Go compiler version, target OS/arch, and the list of supported strong checksum algorithms.`,
		Run:   runVersion,
	})

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runVersion(cmd *cobra.Command, args []string) {
	fmt.Printf("Shuttle v%s\n", versionStr)
	fmt.Printf("  Go:     %s\n", runtime.Version())
	fmt.Printf("  OS:     %s\n", runtime.GOOS)
	fmt.Printf("  Arch:   %s\n", runtime.GOARCH)
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
	if schemaFlag {
		runSchema()
		return
	}
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

func runSchema() {
	fmt.Println(`syncd.yaml Configuration Reference
=====================================

Top-Level Fields
────────────────────────
  version    string    Config version, currently "1.0"
  language   string    UI language: en / zh (default zh)
  checksum   string    Default strong checksum: md5 / sha256 / xxh64 / xxh3 (default xxh64)
  workers    int       Parallel delta workers: 1=serial, 2/4/8=parallel (default 4)
  servers    []Server  Server connection list
  tasks      []Task    Sync task list

Server
────────────────────────
  name       string    Server name, referenced in task.target
  host       string    SSH host address (IP or domain)
  port       int       SSH port (default 22)
  user       string    Login username
  key_file   string    SSH private key path, e.g. ~/.ssh/id_ed25519 (preferred over password)
  password   string    Login password (fallback when key is unavailable; plaintext not recommended)
  protect    []string  Protect patterns (glob) — matching remote files are NEVER overwritten or deleted
                       Example: ["*.db", "*.pem", "config.yaml", "secrets/"]

Task
────────────────────────
  name       string    Task name
  source     string    Local source path.
                       ── Folder ──
                         End with \ or / to sync the folder's CONTENTS into target.
                         Examples:
                           E:\projects\dist\       (Windows)
                           /home/deploy/site/      (Linux/WSL)
                       ── Single file ──
                         No trailing slash.  The file is synced directly.
                         Examples:
                           E:\configs\nginx.conf
                           /etc/myapp/config.yaml
  target     string    Remote target, format: <server name>:<path>
                       ── Folder sync ──
                         End with / to map source contents INTO the directory.
                         Example: myserver:/var/www/html/
                           source=E:\dist\  →  files go to /var/www/html/*
                       ── Single file sync ──
                         No trailing / — the file is placed at exactly this path.
                         Example: myserver:/etc/nginx/nginx.conf
                           source=E:\configs\nginx.conf  →  overwrites /etc/nginx/nginx.conf
  options    Options   Sync options

Options
────────────────────────
  delete     bool      Delete extra files on the remote side (default false)
                       When enabled, remote files not present locally will be removed.
                       ⚠ Only applies to folder syncs.  Ignored for single-file tasks.
  exclude    []string  Glob patterns to skip — matching files/dirs are not transferred
                       Example: ["*.tmp", ".git/", "node_modules/"]
  compress   bool      SSH transport compression (reserved, not yet implemented)
  checksum   bool      Use strong checksums to detect file changes (default false)
                       false: compare by mtime + file size (fast, 1-second precision)
                       true:  compare by full strong checksum (accurate, slower)
  flat       bool      Flat mapping (default false, only meaningful for folder syncs)
                       false: source folder name appears in the target path.
                              E:\projects\dist\  →  /var/www/html/dist/...
                       true:  map source contents directly, no outer folder.
                              E:\projects\dist\  →  /var/www/html/...
  show_dots  bool      Transfer hidden files/directories (default false)
                       Hidden files are those whose name starts with a dot (.)

Strong Checksum Algorithms
────────────────────────
  xxh64      64-bit xxHash (default), fastest — good for LAN/SSD
  xxh3       128-bit xxH3, fast non-crypto hash with wider output (~2⁻⁶⁴ collision)
  md5        128-bit MD5, best cross-platform compatibility
  sha256     256-bit SHA-2, strongest — use when integrity matters most
  (All algorithms have SIMD-accelerated assembly paths on amd64.)

Examples
────────────────────────
  # Folder sync: deploy a website build
  tasks:
    - name: web
      source: E:\projects\dist\
      target: myserver:/var/www/html/
      options:
        delete: true
        exclude: [".DS_Store", "*.map"]

  # Single file sync: push a config file
  tasks:
    - name: nginx-config
      source: E:\configs\nginx.conf
      target: myserver:/etc/nginx/nginx.conf
      options:
        checksum: true

Usage
────────────────────────
  View current config:    shuttle config
  Show this reference:    shuttle config --schema
  Generate a template:    shuttle init`)
}

func runInit(cmd *cobra.Command, args []string) {
	if _, err := os.Stat("syncd.yaml"); err == nil {
		fmt.Println("syncd.yaml already exists")
		return
	}
	os.WriteFile("syncd.yaml", []byte(initTemplate), 0644)
	fmt.Println("Created syncd.yaml — edit it and run 'shuttle push'")
	fmt.Println("Run 'shuttle config --schema' for a full field reference.")
}

const initTemplate = `# Shuttle 同步配置文件
# 用法: shuttle push [任务名]
# 完整参考: shuttle config --schema

version: "1.0"
language: zh               # en / zh
checksum: xxh64            # md5 / sha256 / xxh64 / xxh3
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
  # ── 示例1: 文件夹同步（部署网站）──
  #   source 末尾有 \ → 把文件夹内容映射到 target
  #   target 末尾有 / → 内容放入该目录下
  - name: web
    source: E:\projects\website\dist\
    target: myserver:/var/www/html/
    options:
      delete: true           # 删除远端多余文件（仅文件夹同步有效）
      exclude:
        - "*.tmp"
        - ".DS_Store"
      checksum: false        # false: 比大小+时间  true: 比文件内容哈希
      flat: false            # true: 不套源文件夹名，直接映射内容

  # ── 示例2: 单文件同步（推送配置）──
  #   source 无末尾斜杠 → 视为单文件
  #   target 无末尾斜杠 → 精确覆盖该路径
  # - name: nginx-config
  #   source: E:\configs\nginx.conf
  #   target: myserver:/etc/nginx/nginx.conf
  #   options:
  #     checksum: true       # 单文件建议开启，精确判断是否需要更新
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
