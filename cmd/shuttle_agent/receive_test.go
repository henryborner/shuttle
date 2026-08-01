package main

import (
	"bytes"
	"crypto/sha256"
	"os"
	"path/filepath"
	"testing"

	delta "github.com/henryborner/go-rsync"
)

// wireInstructions produces the wire-encoded instruction stream that
// reconstructs `new` from `old`, followed by the EOS marker.
func wireInstructions(t *testing.T, old, new []byte) []byte {
	t.Helper()
	blockSize := delta.CalculateBlockSize(int64(len(old)))
	sig := delta.GenerateSignature(old, blockSize, delta.GetDefault())
	eng, err := delta.NewMatchEngine(blockSize, delta.GetDefault())
	if err != nil {
		t.Fatalf("NewMatchEngine: %v", err)
	}
	eng.LoadSignature(sig)
	var results []delta.MatchResult
	for _, mr := range eng.Search(new) {
		cp := mr
		if mr.IsLiteral {
			cp.Data = make([]byte, len(mr.Data))
			copy(cp.Data, mr.Data)
		}
		results = append(results, cp)
	}
	var buf bytes.Buffer
	if err := delta.WireEncodeInstructions(&buf, results); err != nil {
		t.Fatalf("WireEncodeInstructions: %v", err)
	}
	buf.Write([]byte{0, 0, 0, 0}) // EOS marker
	return buf.Bytes()
}

// TestReceiveFileReconstruct verifies the full pipeline through the buffered
// writer: scattered single-byte edits force many small literal chunks, and a
// 300KB file exceeds the 64KB buffer, so this exercises Flush correctness.
func TestReceiveFileReconstruct(t *testing.T) {
	dir := t.TempDir()
	old := make([]byte, 300000)
	for i := range old {
		old[i] = byte(i * 7)
	}
	newData := make([]byte, len(old))
	copy(newData, old)
	for _, off := range []int{0, 1, 2, 999, 100000, 150001, 299999} {
		newData[off] ^= 0xFF
	}

	path := filepath.Join(dir, "file.bin")
	if err := os.WriteFile(path, old, 0o644); err != nil {
		t.Fatal(err)
	}

	var sigOut bytes.Buffer
	if err := receiveFile(path, bytes.NewReader(wireInstructions(t, old, newData)), &sigOut, delta.GetDefault(), true); err != nil {
		t.Fatalf("receiveFile: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, newData) {
		t.Fatalf("reconstructed file mismatch: len got=%d want=%d", len(got), len(newData))
	}
	if sigOut.Len() == 0 {
		t.Fatal("expected signature written to stdout")
	}
	// Temp file must be renamed away (no leftover).
	if _, err := os.Stat(path + ".shuttle_tmp"); !os.IsNotExist(err) {
		t.Fatal("temp file left behind after successful receive")
	}
}

func TestReceiveFileVerify(t *testing.T) {
	dir := t.TempDir()
	old := []byte("the quick brown fox jumps over the lazy dog. the quick brown fox jumps over the lazy dog!")
	newData := []byte("the quick brown fox jumps over the lazy dog. the quick brown fox JUMPS over the lazy dog!")
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, old, 0o644); err != nil {
		t.Fatal(err)
	}

	insts := wireInstructions(t, old, newData)
	h := sha256.Sum256(newData)
	insts = append(insts, 0x01) // verify flag
	insts = append(insts, h[:]...)

	var sigOut bytes.Buffer
	if err := receiveFile(path, bytes.NewReader(insts), &sigOut, delta.GetDefault(), true); err != nil {
		t.Fatalf("receiveFile with verify trailer: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, newData) {
		t.Fatal("verify+reconstruct produced wrong file")
	}
}

func TestReceiveFileVerifyMismatch(t *testing.T) {
	dir := t.TempDir()
	old := []byte("aaaaaaaaaaaaaaaaaaaa")
	newData := []byte("bbbbbbbbbbbbbbbbbbbb")
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, old, 0o644); err != nil {
		t.Fatal(err)
	}

	insts := wireInstructions(t, old, newData)
	wrong := sha256.Sum256([]byte("wrong content"))
	insts = append(insts, 0x01)
	insts = append(insts, wrong[:]...)

	var sigOut bytes.Buffer
	if err := receiveFile(path, bytes.NewReader(insts), &sigOut, delta.GetDefault(), true); err == nil {
		t.Fatal("expected verify failure")
	}
	// Original file must remain untouched.
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, old) {
		t.Fatal("original file was overwritten on failed verify")
	}
}

func TestReceiveFileAbandonOnEOF(t *testing.T) {
	dir := t.TempDir()
	old := []byte("hello world hello world hello world")
	path := filepath.Join(dir, "f.txt")
	if err := os.WriteFile(path, old, 0o644); err != nil {
		t.Fatal(err)
	}

	var sigOut bytes.Buffer
	// Truncated stream: valid instructions but no EOS marker — stdin just ends.
	full := wireInstructions(t, old, []byte("hello world goodbye world hello"))
	if len(full) < 5 {
		t.Fatalf("test stream too small")
	}
	err := receiveFile(path, bytes.NewReader(full[:len(full)-4]), &sigOut, delta.GetDefault(), true)
	if err != nil {
		t.Fatalf("EOF should be treated as abandon (nil error), got: %v", err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, old) {
		t.Fatal("original file modified on abandoned receive")
	}
	if _, err := os.Stat(path + ".shuttle_tmp"); !os.IsNotExist(err) {
		t.Fatal("temp file not cleaned up on abandoned receive")
	}
}
