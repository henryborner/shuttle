package main

import (
	"fmt"
	"os"

	"github.com/henryborner/shuttle/internal/config"
	"github.com/henryborner/shuttle/internal/transport"
	"github.com/henryborner/shuttle/internal/util"
)

// doSync 执行同步任务
func doSync(taskName, cfgPath string, dryRun bool) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "❌ 加载配置失败: %v\n", err)
		os.Exit(1)
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "❌ 配置无效: %v\n", err)
		os.Exit(1)
	}

	var tasks []config.Task
	if taskName != "" {
		t := cfg.GetTask(taskName)
		if t == nil {
			fmt.Fprintf(os.Stderr, "❌ 找不到任务: %s\n", taskName)
			os.Exit(1)
		}
		tasks = append(tasks, *t)
	} else {
		tasks = cfg.Tasks
	}

	if dryRun {
		fmt.Println("🔍 模拟运行 — 不会实际修改任何文件")
		fmt.Println()
	}

	for _, task := range tasks {
		fmt.Printf("📦 任务: %s\n    %s\n   目标: %s\n", task.Name, task.Source, task.Target)

		if dryRun {
			fmt.Println("   [模拟] 跳过")
			fmt.Println()
			continue
		}

		serverName, remotePath := config.ParseTarget(task.Target)
		if serverName == "" {
			fmt.Println("   ⚠️  本地同步暂未实现")
			continue
		}

		server := cfg.GetServer(serverName)
		if server == nil {
			fmt.Fprintf(os.Stderr, "   ❌ 找不到服务器: %s\n", serverName)
			continue
		}

		// 连接
		fmt.Printf("   🔌 连接 %s@%s:%d...\n", server.User, server.Host, server.Port)
		sftp := transport.NewSFTP(transport.SFTPConfig{
			Host: server.Host, Port: server.Port,
			User: server.User, KeyFile: server.KeyFile, Pass: server.Pass,
			AgentPath: server.AgentPath,
		})

		if err := sftp.Connect(); err != nil {
			fmt.Fprintf(os.Stderr, "   ❌ 连接失败: %v\n", err)
			continue
		}

		// 同步
		engine := transport.NewSyncEngine(sftp)
		// TODO: 接入 TUI Hook: engine.SetHook(tuiHook)
		stats, err := engine.Sync(transport.SyncOptions{
			Source:   task.Source,
			Target:   remotePath,
			Delete:   task.Options.Delete,
			Exclude:  task.Options.Exclude,
			Checksum: task.Options.Checksum,
			DryRun:   dryRun,
			SkipDots: true,
			Workers:  cfg.Workers,
		})

		sftp.Close()

		if err != nil {
			fmt.Fprintf(os.Stderr, "   ❌ 同步失败: %v\n", err)
			continue
		}

		// 统计输出
		fmt.Printf("   ✅ 完成 | 文件:%d 新增:%d 更新:%d",
			stats.TotalFiles, stats.NewFiles, stats.UpdatedFiles)
		if stats.SkippedFiles > 0 {
			fmt.Printf(" 跳过:%d", stats.SkippedFiles)
		}
		if stats.DeltaFiles > 0 {
			fmt.Printf(" Δ:%d", stats.DeltaFiles)
		}
		fmt.Printf(" | %s", util.FormatBytes(stats.TotalBytes))
		if stats.DeltaSaved > 0 {
			fmt.Printf(" 💾 增量节省:%s (%.0f%%)",
				util.FormatBytes(stats.DeltaSaved),
				float64(stats.DeltaSaved)/float64(stats.TotalBytes)*100)
		}
		if len(stats.Errors) > 0 {
			fmt.Printf(" | ⚠️  错误:%d", len(stats.Errors))
		}
		fmt.Println()
	}

	if dryRun {
		fmt.Println("🔍 模拟运行结束 — 使用 'shuttle push' 执行实际同步")
	}
}
