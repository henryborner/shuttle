package util

import (
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// standardKeyNames lists SSH private key names to try in ~/.ssh, in priority order.
var standardKeyNames = []string{"id_ed25519", "id_rsa", "id_ecdsa"}

// ReadSSHKey tries to read and parse an SSH private key.
// If keyPath is non-empty it is tried first; otherwise standard ~/.ssh keys are tried.
// Returns the parsed signer, or an error if no key could be loaded.
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
			lastErr = err
			continue
		}
		return signer, nil
	}
	return nil, fmt.Errorf("无法读取 SSH 密钥: %w", lastErr)
}

// CheckHostKey returns an ssh.HostKeyCallback that verifies the host key
// against the user's ~/.ssh/known_hosts file. Unknown hosts are automatically
// added (trust-on-first-use). Changed keys are rejected with an error.
func CheckHostKey() ssh.HostKeyCallback {
	home, err := os.UserHomeDir()
	if err != nil {
		log.Printf("known_hosts: cannot find home dir, host key check disabled")
		return ssh.InsecureIgnoreHostKey()
	}

	khPath := filepath.Join(home, ".ssh", "known_hosts")
	// Ensure the file exists (create empty if not)
	if _, err := os.Stat(khPath); os.IsNotExist(err) {
		f, err := os.OpenFile(khPath, os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			log.Printf("known_hosts: cannot create %s, host key check disabled", khPath)
			return ssh.InsecureIgnoreHostKey()
		}
		f.Close()
	}

	baseCb, err := knownhosts.New(khPath)
	if err != nil {
		log.Printf("known_hosts: %v, host key check disabled", err)
		return ssh.InsecureIgnoreHostKey()
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
