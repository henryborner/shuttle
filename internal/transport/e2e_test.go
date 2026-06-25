package transport

import (
	"bytes"
	"crypto/rand"
	"fmt"
	mrand "math/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"

	delta "github.com/henryborner/go-rsync"
	"golang.org/x/crypto/ssh"
)

func sshClient(t *testing.T) *ssh.Client {
	t.Helper()
	host := os.Getenv("SHUTTLE_TEST_HOST")
	if host == "" {
		t.Skip("SHUTTLE_TEST_HOST not set")
	}
	home, _ := os.UserHomeDir()
	keyPath := filepath.Join(home, ".ssh", "id_ed25519")
	key, err := os.ReadFile(keyPath)
	if err != nil {
		keyPath = filepath.Join(home, ".ssh", "id_rsa")
		key, err = os.ReadFile(keyPath)
		if err != nil {
			t.Skipf("no SSH key: %v", err)
		}
	}
	signer, _ := ssh.ParsePrivateKey(key)
	cfg := &ssh.ClientConfig{
		User: "root", Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
	}
	c, err := ssh.Dial("tcp", host+":22", cfg)
	if err != nil {
		t.Skipf("SSH: %v", err)
	}
	return c
}

func TestE2EDeltaSmall(t *testing.T) {
	client := sshClient(t)
	defer client.Close()
	runDeltaTest(t, client, "/tmp/shuttle_e2e_small.txt",
		"Line 1\nLine 2 unchanged\nLine 3 original\n",
		"Line 1\nLine 2 unchanged\nLine 3 MODIFIED!\n")
}

func TestE2EDeltaLarge(t *testing.T) {
	client := sshClient(t)
	defer client.Close()

	// 1MB random data
	_ = rand.Reader
	oldData := make([]byte, 1024*1024)
	for i := range oldData {
		oldData[i] = byte('a' + mrand.Intn(26))
	}

	// write to remote
	s, _ := client.NewSession()
	s.Stdin = bytes.NewReader(oldData)
	s.Run("cat > /tmp/shuttle_e2e_large.dat")
	s.Close()

	// new file: insert data in the middle
	newData := make([]byte, len(oldData)+2048)
	copy(newData, oldData[:len(oldData)/2])
	insert := []byte("[[[SHUTTLE DELTA INSERTED DATA]]]")
	copy(newData[len(oldData)/2:], insert)
	copy(newData[len(oldData)/2+len(insert):], oldData[len(oldData)/2:])

	t.Logf("old: %d, new: %d, inserted: %d bytes", len(oldData), len(newData), len(insert))

	sent, saved := doDelta(t, client, "/tmp/shuttle_e2e_large.dat", newData)

	t.Logf("━━━━━━━━━━━━━━━━━━━━━━")
	t.Logf("  delta transfer complete")
	t.Logf("   new file: %.1f KB", float64(len(newData))/1024)
	t.Logf("   sent:   %.1f KB", float64(sent)/1024)
	t.Logf("   saved:  %.1f KB (%.1f%%)", float64(saved)/1024, float64(saved)/float64(len(newData))*100)
	t.Logf("━━━━━━━━━━━━━━━━━━━━━━")

	cleanup(t, client, "/tmp/shuttle_e2e_large.dat")
}

func runDeltaTest(t *testing.T, client *ssh.Client, path, old, new string) {
	s, _ := client.NewSession()
	s.Run(fmt.Sprintf("printf '%%s' '%s' > %s", old, path))
	s.Close()
	doDelta(t, client, path, []byte(new))
	t.Log("small file delta test passed")
	cleanup(t, client, path)
}

func doDelta(t *testing.T, client *ssh.Client, path string, newData []byte) (sent, saved int64) {
	session, _ := client.NewSession()
	stdin, _ := session.StdinPipe()
	stdout, _ := session.StdoutPipe()
	stderr, _ := session.StderrPipe()
	session.Start("/usr/local/bin/shuttle receive '" + strings.ReplaceAll(path, "'", "'\\''") + "'")

	sig, err := delta.WireDecodeSignature(stdout)
	if err != nil {
		t.Fatalf("sig: %v", err)
	}

	eng := delta.NewMatchEngine(sig.BlockSize, delta.GetDefault())
	eng.LoadSignature(sig)
	insts := eng.Search(newData)

	delta.WireEncodeInstructions(stdin, insts)
	stdin.Close()

	var errBuf bytes.Buffer
	errBuf.ReadFrom(stderr)
	session.Wait()

	if errBuf.Len() > 0 {
		t.Logf("remote stderr: %s", errBuf.String())
	}

	saved = int64(len(newData)) - eng.LiteralBytes
	return eng.LiteralBytes, saved
}

func cleanup(_ *testing.T, client *ssh.Client, path string) {
	s, _ := client.NewSession()
	s.Run("rm -f " + path)
	s.Close()
}
