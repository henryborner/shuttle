package main

import (
	"fmt"
	"os"
	"time"

	"github.com/henryborner/shuttle/internal/util"
	"golang.org/x/crypto/ssh"
)

func testDial(host string, port int, user, keyFile, pass string) error {
	authMethods := []ssh.AuthMethod{}
	if keyFile != "" {
		key, err := os.ReadFile(keyFile)
		if err == nil {
			signer, err := ssh.ParsePrivateKey(key)
			if err == nil {
				authMethods = append(authMethods, ssh.PublicKeys(signer))
			}
		}
	}
	for _, kf := range []string{
		os.ExpandEnv("$HOME/.ssh/id_ed25519"),
		os.ExpandEnv("$HOME/.ssh/id_rsa"),
		os.ExpandEnv("$USERPROFILE/.ssh/id_ed25519"),
		os.ExpandEnv("$USERPROFILE/.ssh/id_rsa"),
	} {
		key, err := os.ReadFile(kf)
		if err != nil {
			continue
		}
		signer, err := ssh.ParsePrivateKey(key)
		if err != nil {
			continue
		}
		authMethods = append(authMethods, ssh.PublicKeys(signer))
	}
	if pass != "" {
		authMethods = append(authMethods, ssh.Password(pass))
	}

	config := &ssh.ClientConfig{
		User:            user,
		Auth:            authMethods,
		HostKeyCallback: util.CheckHostKey(),
		Timeout:         10 * time.Second,
	}

	addr := fmt.Sprintf("%s:%d", host, port)
	client, err := ssh.Dial("tcp", addr, config)
	if err != nil {
		return err
	}
	client.Close()
	return nil
}
