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
		fmt.Fprintf(os.Stderr, "Failed to load config: %v\n", err)
		os.Exit(1)
	}
	if err := cfg.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "Invalid config: %v\n", err)
		os.Exit(1)
	}

	var tasks []config.Task
	if taskName != "" {
		t := cfg.GetTask(taskName)
		if t == nil {
			fmt.Fprintf(os.Stderr, "Task not found: %s\n", taskName)
			os.Exit(1)
		}
		tasks = append(tasks, *t)
	} else {
		tasks = cfg.Tasks
	}

	if dryRun {
		fmt.Println("Dry run — no changes will be made")
		fmt.Println()
	}

	for _, task := range tasks {
		fmt.Printf("Task: %s\n  Source: %s\n  Target: %s\n", task.Name, task.Source, task.Target)

		if dryRun {
			fmt.Println("  [dry run] skipped")
			fmt.Println()
			continue
		}

		serverName, remotePath := config.ParseTarget(task.Target)
		if serverName == "" {
			fmt.Println("  Local sync not yet supported")
			continue
		}

		server := cfg.GetServer(serverName)
		if server == nil {
			fmt.Fprintf(os.Stderr, "  Server not found: %s\n", serverName)
			continue
		}

		// 连接
		fmt.Printf("  Connecting %s@%s:%d...\n", server.User, server.Host, server.Port)
		sftp := transport.NewSFTP(transport.SFTPConfig{
			Host: server.Host, Port: server.Port,
			User: server.User, KeyFile: server.KeyFile, Pass: server.Pass,
		})

		if err := sftp.Connect(); err != nil {
			fmt.Fprintf(os.Stderr, "  Connect failed: %v\n", err)
			continue
		}

		// 同步
		engine := transport.NewSyncEngine(sftp)
		stats, err := engine.Sync(transport.SyncOptions{
			Source:   task.Source,
			Target:   remotePath,
			Delete:   task.Options.Delete,
			Exclude:  task.Options.Exclude,
			Checksum: task.Options.Checksum,
			DryRun:   dryRun,
			SkipDots: true,
			Workers:  cfg.Workers,
			Flat:     task.Options.Flat,
		})

		sftp.Close()

		if err != nil {
			fmt.Fprintf(os.Stderr, "  Sync failed: %v\n", err)
			continue
		}

		fmt.Printf("  Done | files:%d new:%d updated:%d",
			stats.TotalFiles, stats.NewFiles, stats.UpdatedFiles)
		if stats.SkippedFiles > 0 {
			fmt.Printf(" skipped:%d", stats.SkippedFiles)
		}
		if stats.DeltaFiles > 0 {
			fmt.Printf(" Δ:%d", stats.DeltaFiles)
		}
		fmt.Printf(" | %s", util.FormatBytes(stats.TotalBytes))
		if stats.DeltaSaved > 0 {
			fmt.Printf("  saved:%s (%.0f%%)",
				util.FormatBytes(stats.DeltaSaved),
				float64(stats.DeltaSaved)/float64(stats.TotalBytes)*100)
		}
		if len(stats.Errors) > 0 {
			fmt.Printf(" | errors:%d", len(stats.Errors))
		}
		fmt.Println()
	}

	if dryRun {
		fmt.Println("Dry run complete — use 'shuttle push' to sync")
	}
}
