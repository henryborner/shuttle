// Package agent manages the remote shuttle_linux agent deployment.
// agent 包管理远端 shuttle_linux agent 的部署、检测和清理。
package agent

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/henryborner/shuttle/internal/config"
	"github.com/henryborner/shuttle/internal/util"
	"golang.org/x/crypto/ssh"
)

// RemotePaths lists candidate installation paths on the remote server,
// in priority order (system → user).
// RemotePaths 远端 agent 候选安装路径，优先级从系统到用户。
var RemotePaths = []string{
	"/usr/local/bin/shuttle",
	"$HOME/shuttle",
}

// shellPath quotes a path for safe use in a remote shell command.
// Literal paths are single-quoted; paths containing $ (e.g. $HOME) are
// left unquoted so the shell expands the variable.
// shellPath 对路径做安全转义，字面量路径加单引号，含 $ 的路径保持原样由 shell 展开。
func shellPath(p string) string {
	if strings.Contains(p, "$") {
		return p
	}
	return "'" + strings.ReplaceAll(p, "'", "'\\''") + "'"
}

// FindResult describes a verified shuttle agent installation.
// FindResult 描述一个已验证的 shuttle agent 安装。
type FindResult struct {
	Path    string // installed path / 安装路径
	Version string // version output / 版本信息
}

// Deploy uploads shuttle_linux to the remote server and returns the installed
// path and version output on success.
// Deploy 将 shuttle_linux 上传到远端服务器，成功时返回安装路径和版本信息。
func Deploy(srv config.Server) (path string, version string, err error) {
	client, err := dial(srv, 15*time.Second)
	if err != nil {
		return "", "", err
	}
	defer client.Close()

	localBin, err := findLocalBinary()
	if err != nil {
		return "", "", err
	}
	binData, err := os.ReadFile(localBin)
	if err != nil {
		return "", "", fmt.Errorf("read %s: %w", localBin, err)
	}

	type deployPath struct {
		path string
		cmd  string
	}
	// Build deploy paths from RemotePaths to keep a single source of truth.
	// 从 RemotePaths 生成部署路径，保持单一数据源。
	paths := make([]deployPath, 0, len(RemotePaths))
	for _, p := range RemotePaths {
		cmd := "cat > " + shellPath(p) + " && chmod +x " + shellPath(p)
		if p == "$HOME/shuttle" {
			cmd += " && grep -q 'export PATH=$PATH:$HOME' $HOME/.bashrc 2>/dev/null || echo 'export PATH=$PATH:$HOME' >> $HOME/.bashrc"
		}
		paths = append(paths, deployPath{path: p, cmd: cmd})
	}

	var lastErr error
	for _, dp := range paths {
		s, err := client.NewSession()
		if err != nil {
			lastErr = err
			continue
		}
		stdin, err := s.StdinPipe()
		if err != nil {
			lastErr = err
			s.Close()
			continue
		}
		if err := s.Start(dp.cmd); err != nil {
			lastErr = err
			stdin.Close()
			s.Close()
			continue
		}
		if _, err := io.Copy(stdin, bytes.NewReader(binData)); err != nil {
			lastErr = err
			stdin.Close()
			s.Close()
			continue
		}
		stdin.Close()
		if err := s.Wait(); err != nil {
			lastErr = err
			s.Close()
			continue
		}
		s.Close()

		// Verify with identify first (machine check).
		// 先用 identify 做机器验证。
		idOut, err := runRemoteCmd(client, shellPath(dp.path)+" identify")
		if err != nil {
			lastErr = fmt.Errorf("identify command failed: %w", err)
			if _, cleanErr := runRemoteCmd(client, "rm -f "+shellPath(dp.path)); cleanErr != nil {
				lastErr = fmt.Errorf("%w (cleanup also failed: %v)", lastErr, cleanErr)
			}
			continue
		}
		if !strings.HasPrefix(strings.TrimSpace(idOut), "SHuTtL3_AgEnT_lD:") {
			lastErr = fmt.Errorf("identify output mismatch: got %q", strings.TrimSpace(idOut))
			if _, cleanErr := runRemoteCmd(client, "rm -f "+shellPath(dp.path)); cleanErr != nil {
				lastErr = fmt.Errorf("%w (cleanup also failed: %v)", lastErr, cleanErr)
			}
			continue
		}
		// Get human-readable version string for display.
		// 用 version 获取人类可读的版本信息展示。
		verOut, _ := runRemoteCmd(client, shellPath(dp.path)+" version")
		return dp.path, strings.TrimSpace(verOut), nil
	}
	return "", "", fmt.Errorf("deploy failed: %w", lastErr)
}

// Check returns true if the shuttle agent is installed and reachable on the
// remote server.
// Check 检查远端服务器上是否已安装 shuttle agent。
func Check(srv config.Server) (bool, error) {
	r, err := Find(srv)
	return r != nil, err
}

// Find searches common remote paths for a shuttle agent binary.
// Each candidate is verified by running "<path> identify" and checking for the
// unique agent identifier prefix ("SHuTtL3_AgEnT_lD:") — no other software
// would produce this output, preventing false positives from unrelated binaries
// that happen to be named "shuttle".
//
// Returns nil if no verified agent is found.
//
// Find 在远端搜索 shuttle agent，通过 identify 命令验证唯一标识前缀
// （"SHuTtL3_AgEnT_lD:"），避免误删同名无关二进制。未找到时返回 nil。
func Find(srv config.Server) (*FindResult, error) {
	client, err := dial(srv, 8*time.Second)
	if err != nil {
		return nil, err
	}
	defer client.Close()

	for _, p := range RemotePaths {
		// Verify with identify first (machine check).
		// 先用 identify 做机器验证。
		idOut, err := runRemoteCmd(client, shellPath(p)+" identify")
		if err != nil || !strings.HasPrefix(strings.TrimSpace(idOut), "SHuTtL3_AgEnT_lD:") {
			continue
		}
		// Get human-readable version string for display.
		// 用 version 获取人类可读的版本信息展示。
		verOut, _ := runRemoteCmd(client, shellPath(p)+" version")
		return &FindResult{Path: p, Version: strings.TrimSpace(verOut)}, nil
	}
	return nil, nil
}

// Remove finds and removes the shuttle agent from the remote server.
// If found is non-nil, it reuses the pre-existing FindResult (avoiding a
// second SSH round-trip). Only deletes binaries confirmed to be Shuttle.
//
// Remove 查找并删除远端 shuttle agent。
// found 非 nil 时复用已有查找结果（避免二次 SSH），只删除验证为 Shuttle 的二进制。
func Remove(srv config.Server, found *FindResult) error {
	r := found
	if r == nil {
		var err error
		r, err = Find(srv)
		if err != nil {
			return err
		}
		if r == nil {
			return fmt.Errorf("no shuttle agent found on remote")
		}
	}

	client, err := dial(srv, 8*time.Second)
	if err != nil {
		return err
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	defer session.Close()
	if err := session.Run("rm -f " + shellPath(r.Path)); err != nil {
		return fmt.Errorf("remove %s: %w", r.Path, err)
	}
	return nil
}

// dial opens an SSH connection to the server.
func dial(srv config.Server, timeout time.Duration) (*ssh.Client, error) {
	authMethods := util.BuildAuthMethods(srv.KeyFile, srv.Pass)
	if len(authMethods) == 0 {
		return nil, fmt.Errorf("no authentication methods available")
	}
	port := srv.Port
	if port <= 0 {
		port = 22
	}
	cfg := &ssh.ClientConfig{
		User: srv.User, Auth: authMethods,
		HostKeyCallback: util.CheckHostKey(), Timeout: timeout,
	}
	return ssh.Dial("tcp", fmt.Sprintf("%s:%d", strings.TrimSpace(srv.Host), port), cfg)
}

// findLocalBinary locates shuttle_linux next to the current executable.
func findLocalBinary() (string, error) {
	exePath, err := os.Executable()
	if err == nil {
		localBin := filepath.Join(filepath.Dir(exePath), "shuttle_linux")
		if _, err := os.Stat(localBin); err == nil {
			return localBin, nil
		}
	}
	// Fallback: look in current working directory.
	if _, err := os.Stat("shuttle_linux"); err == nil {
		return "shuttle_linux", nil
	}
	return "", fmt.Errorf("shuttle_linux not found (place it next to shuttle.exe)")
}

// runRemoteCmd executes a command on the remote server and returns stdout.
func runRemoteCmd(client *ssh.Client, cmd string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	out, err := session.Output(cmd)
	session.Close()
	return string(out), err
}
