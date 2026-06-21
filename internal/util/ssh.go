package util

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
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
