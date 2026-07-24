package main

import (
	"fmt"
	"time"

	"github.com/henryborner/shuttle/internal/util"
	"golang.org/x/crypto/ssh"
)

func testDial(host string, port int, user, keyFile, pass string) error {
	authMethods := util.BuildAuthMethods(keyFile, pass)
	if len(authMethods) == 0 {
		return fmt.Errorf("no authentication methods available")
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
