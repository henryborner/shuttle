package util

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// SSHKeepAlive is the keepalive interval applied to long-lived SSH client
// connections (see StartKeepAlive).
// SSHKeepAlive 是长连接 SSH 客户端的 keepalive 间隔（见 StartKeepAlive）。
const SSHKeepAlive = 30 * time.Second

// StartKeepAlive launches a background goroutine that periodically sends SSH
// keepalive requests (keepalive@openssh.com) until the returned stop function
// is called or the connection dies. This keeps long transfers alive through
// NAT idle timeouts and surfaces silent network drops (dropped VPN/WiFi)
// promptly instead of hanging until the TCP timeout.
//
// Newer golang.org/x/crypto/ssh removed ClientConfig.KeepAlivePeriod, so the
// keepalive must be sent manually via SendRequest.
// StartKeepAlive 启动后台 goroutine 定期发送 SSH keepalive 请求，直到调用
// 返回的 stop 函数或连接失败为止。它让长传穿越 NAT 空闲超时，并在网络静默
// 断开（VPN/WiFi 掉线）时及时感知，而不是挂到 TCP 超时。
// 新版 x/crypto/ssh 已移除 ClientConfig.KeepAlivePeriod，需用 SendRequest 手动发送。
func StartKeepAlive(client *ssh.Client) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		t := time.NewTicker(SSHKeepAlive)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				// wantReply=true forces an ACK from the server, so a dead
				// connection surfaces as an error here right away.
				if _, _, err := client.SendRequest("keepalive@openssh.com", true, nil); err != nil {
					return // connection is dead; stop trying
				}
			}
		}
	}()
	return func() {
		select {
		case <-stop: // already stopped
		default:
			close(stop)
		}
		// Wait for the goroutine to exit, but never block Close() for long
		// even if SendRequest is stuck on a dead connection.
		select {
		case <-done:
		case <-time.After(2 * time.Second):
		}
	}
}

// standardKeyNames lists SSH private key names to try in ~/.ssh, in priority order.
var standardKeyNames = []string{"id_ed25519", "id_rsa", "id_ecdsa"}

// ReadSSHKey tries to read and parse an SSH private key.
// If keyPath is non-empty it is tried first; otherwise standard ~/.ssh keys are tried.
// Returns the parsed signer, or an error if no key could be loaded.
// ReadSSHKey 尝试读取并解析 SSH 私钥。优先使用指定路径，其次尝试 ~/.ssh/ 下的标准密钥。
func ReadSSHKey(keyPath string) (ssh.Signer, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}

	paths := make([]string, 0, 4)
	if keyPath != "" {
		paths = append(paths, keyPath)
	}
	if home != "" {
		for _, name := range standardKeyNames {
			paths = append(paths, filepath.Join(home, ".ssh", name))
		}
	}

	var lastErr error
	for _, p := range paths {
		key, err := os.ReadFile(p)
		if err != nil {
			lastErr = err
			continue
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			// If passphrase-protected, try empty passphrase.
			// 如果有密码保护，尝试空密码。
			if strings.Contains(err.Error(), "passphrase") || strings.Contains(err.Error(), "encrypted") {
				signer, err = ssh.ParsePrivateKeyWithPassphrase(key, []byte{})
			}
		}
		if err != nil {
			lastErr = fmt.Errorf("%s: %w", filepath.Base(p), err)
			// If user explicitly specified this key, fail fast instead of silently falling back
			if keyPath != "" && p == keyPath {
				return nil, lastErr
			}
			continue
		}
		return signer, nil
	}
	return nil, fmt.Errorf("无法读取 SSH 密钥: %w", lastErr)
}

// BuildAuthMethods builds SSH auth methods from configured key + optional password.
// Key auth is tried first, then password as fallback.
// BuildAuthMethods 根据配置的密钥和可选密码构建 SSH 认证方法列表（密钥优先，密码备用）。
func BuildAuthMethods(keyPath, password string) []ssh.AuthMethod {
	var methods []ssh.AuthMethod
	signer, err := ReadSSHKey(keyPath)
	if err == nil {
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if password != "" {
		methods = append(methods, ssh.Password(password))
	}
	return methods
}

// CheckHostKey returns an ssh.HostKeyCallback that verifies the host key
// against the user's ~/.ssh/known_hosts file. Unknown hosts are automatically
// added (trust-on-first-use). Changed keys are rejected.
// CheckHostKey 返回 host key 验证回调：自动添加未知主机（TOFU），拒绝已变更的 key。
func CheckHostKey() ssh.HostKeyCallback {
	home, err := os.UserHomeDir()
	if err != nil {
		return hostKeyUnavailable("cannot find home directory")
	}

	khPath := filepath.Join(home, ".ssh", "known_hosts")
	if _, err := os.Stat(khPath); os.IsNotExist(err) {
		f, err := os.OpenFile(khPath, os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			return hostKeyUnavailable(fmt.Sprintf("cannot create %s", khPath))
		}
		f.Close()
	}

	baseCb, err := knownhosts.New(khPath)
	if err != nil {
		return hostKeyUnavailable(fmt.Sprintf("cannot parse known_hosts: %v", err))
	}

	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := baseCb(hostname, remote, key)
		if err == nil {
			return nil // key matched
		}
		// If key is unknown (not in file), add it (TOFU).
		var kErr *knownhosts.KeyError
		if errors.As(err, &kErr) && len(kErr.Want) == 0 {
			f, ferr := os.OpenFile(khPath, os.O_APPEND|os.O_WRONLY, 0600)
			if ferr != nil {
				return fmt.Errorf("known_hosts: cannot append: %w", ferr)
			}
			defer f.Close()
			line := knownhosts.Line([]string{hostname}, key)
			if _, ferr := fmt.Fprintln(f, line); ferr != nil {
				return fmt.Errorf("known_hosts: cannot write: %w", ferr)
			}
			return nil // TOFU accepted
		}
		// Key changed → reject
		return fmt.Errorf("主机密钥不匹配! 可能是中间人攻击: %w", err)
	}
}

// hostKeyUnavailable returns a callback that rejects all connections
// with an error message explaining why host key verification is unavailable.
func hostKeyUnavailable(reason string) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		return fmt.Errorf("主机密钥验证不可用 (%s) — 拒绝连接以防中间人攻击", reason)
	}
}
