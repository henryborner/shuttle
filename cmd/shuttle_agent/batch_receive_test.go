package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	delta "github.com/henryborner/go-rsync"
)

// batchFrame builds one length-prefixed frame (client or agent side).
func batchFrame(t *testing.T, typ byte, payload []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	hdr := []byte{typ, 0, 0, 0, 0}
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	buf.Write(hdr)
	buf.Write(payload)
	return buf.Bytes()
}

// wireBatch produces ONE count-prefixed instruction batch (no EOS marker —
// the batch protocol uses an explicit 0x00 frame for EOS).
func wireBatch(t *testing.T, old, new []byte) []byte {
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
	return buf.Bytes()
}

// buildBatchFile builds the client→agent frames for one file: begin +
// one instruction batch + EOS (+ optional verify).
func buildBatchFile(t *testing.T, path string, instr []byte, verify *[32]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write(batchFrame(t, frameBeginFile, []byte(path)))
	buf.Write(batchFrame(t, frameInstr, instr))
	buf.Write(batchFrame(t, frameEOS, nil))
	if verify != nil {
		payload := append([]byte{1}, verify[:]...)
		buf.Write(batchFrame(t, frameVerify, payload))
	}
	return buf.Bytes()
}

func makeOldNew(n int, seed byte) (old, newData []byte) {
	old = make([]byte, n)
	newData = make([]byte, n)
	for i := range old {
		old[i] = byte(i*7 + int(seed))
	}
	copy(newData, old)
	for _, off := range []int{0, 1, n / 2, n - 1} {
		if off >= 0 && off < n {
			newData[off] ^= 0xFF
		}
	}
	return
}

// TestReceiveBatchBasic: two files through one batch, both reconstructed.
func TestReceiveBatchBasic(t *testing.T) {
	dir := t.TempDir()
	type fc struct{ name string; old, new []byte }
	oldA, newA := makeOldNew(1000, 1)
	oldB, newB := makeOldNew(300000, 9)
	files := []fc{
		{"a.bin", oldA, newA},
		{"b.bin", oldB, newB},
	}

	var input bytes.Buffer
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f.name), f.old, 0o644); err != nil {
			t.Fatal(err)
		}
		input.Write(buildBatchFile(t, filepath.Join(dir, f.name), wireBatch(t, f.old, f.new), nil))
	}
	input.Write(batchFrame(t, frameEndBatch, nil))

	var out bytes.Buffer
	if err := receiveBatch(bytes.NewReader(input.Bytes()), &out, delta.GetDefault(), true); err != nil {
		t.Fatalf("receiveBatch: %v", err)
	}

	fr := &frameReader{r: bytes.NewReader(out.Bytes())}
	for _, f := range files {
		typ, payload, err := fr.read()
		if err != nil {
			t.Fatalf("read sig frame: %v", err)
		}
		if typ != frameSig || len(payload) == 0 {
			t.Fatalf("expected sig frame, got 0x%02x len=%d", typ, len(payload))
		}
		if _, err := delta.WireDecodeSignature(bytes.NewReader(payload)); err != nil {
			t.Fatalf("decode sig: %v", err)
		}
		typ, payload, err = fr.read()
		if err != nil {
			t.Fatalf("read result frame: %v", err)
		}
		if typ != frameResult || len(payload) != 1 || payload[0] != 0 {
			t.Fatalf("expected ok result, got 0x%02x %v", typ, payload)
		}
		got, err := os.ReadFile(filepath.Join(dir, f.name))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, f.new) {
			t.Fatalf("%s reconstructed mismatch: len got=%d want=%d", f.name, len(got), len(f.new))
		}
	}
	typ, payload, err := fr.read()
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	if typ != frameSummary || len(payload) != 8 {
		t.Fatalf("expected summary frame, got 0x%02x len=%d", typ, len(payload))
	}
	if ok := binary.BigEndian.Uint32(payload[0:4]); ok != 2 {
		t.Fatalf("summary ok=%d want 2", ok)
	}
	if fail := binary.BigEndian.Uint32(payload[4:8]); fail != 0 {
		t.Fatalf("summary fail=%d want 0", fail)
	}
}

// TestReceiveBatchVerify: verify trailer per file is checked.
func TestReceiveBatchVerify(t *testing.T) {
	dir := t.TempDir()
	old, newData := makeOldNew(5000, 3)
	path := filepath.Join(dir, "v.bin")
	if err := os.WriteFile(path, old, 0o644); err != nil {
		t.Fatal(err)
	}

	h := sha256.Sum256(newData)
	var input bytes.Buffer
	input.Write(buildBatchFile(t, path, wireBatch(t, old, newData), &h))
	input.Write(batchFrame(t, frameEndBatch, nil))

	var out bytes.Buffer
	if err := receiveBatch(bytes.NewReader(input.Bytes()), &out, delta.GetDefault(), true); err != nil {
		t.Fatalf("receiveBatch: %v", err)
	}

	fr := &frameReader{r: bytes.NewReader(out.Bytes())}
	if typ, _, err := fr.read(); err != nil || typ != frameSig {
		t.Fatalf("expected sig frame, got 0x%02x err=%v", typ, err)
	}
	if typ, payload, err := fr.read(); err != nil || typ != frameResult || len(payload) != 1 || payload[0] != 0 {
		t.Fatalf("expected ok result, got 0x%02x %v err=%v", typ, payload, err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, newData) {
		t.Fatal("verify+reconstruct produced wrong file")
	}
}

// TestReceiveBatchErrorContinues: a failing file reports an error frame and
// the batch continues with the next file.
func TestReceiveBatchErrorContinues(t *testing.T) {
	dir := t.TempDir()

	good, gNew := makeOldNew(200, 5)
	goodPath := filepath.Join(dir, "good.bin")
	if err := os.WriteFile(goodPath, good, 0o644); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "does-not-exist.bin")

	var input bytes.Buffer
	// missing file first: only a begin frame (client sees the error and
	// sends no instructions for it)
	input.Write(batchFrame(t, frameBeginFile, []byte(missing)))
	// then a good file
	input.Write(buildBatchFile(t, goodPath, wireBatch(t, good, gNew), nil))
	input.Write(batchFrame(t, frameEndBatch, nil))

	var out bytes.Buffer
	if err := receiveBatch(bytes.NewReader(input.Bytes()), &out, delta.GetDefault(), true); err != nil {
		t.Fatalf("receiveBatch: %v", err)
	}

	fr := &frameReader{r: bytes.NewReader(out.Bytes())}
	// missing file: error result frame (status 1)
	if typ, payload, err := fr.read(); err != nil || typ != frameResult || len(payload) == 0 || payload[0] != 1 {
		t.Fatalf("expected error result for missing file, got 0x%02x %v err=%v", typ, payload, err)
	}
	// good file: sig + ok
	if typ, _, err := fr.read(); err != nil || typ != frameSig {
		t.Fatalf("expected sig frame, got 0x%02x err=%v", typ, err)
	}
	if typ, payload, err := fr.read(); err != nil || typ != frameResult || len(payload) != 1 || payload[0] != 0 {
		t.Fatalf("expected ok result, got 0x%02x %v err=%v", typ, payload, err)
	}
	// summary: 1 ok, 1 fail
	if typ, payload, err := fr.read(); err != nil || typ != frameSummary || len(payload) != 8 {
		t.Fatalf("expected summary, got 0x%02x err=%v", typ, err)
	} else if ok := binary.BigEndian.Uint32(payload[0:4]); ok != 1 {
		t.Fatalf("summary ok=%d want 1", ok)
	} else if fail := binary.BigEndian.Uint32(payload[4:8]); fail != 1 {
		t.Fatalf("summary fail=%d want 1", fail)
	}
	got, _ := os.ReadFile(goodPath)
	if !bytes.Equal(got, gNew) {
		t.Fatal("good file not reconstructed after error")
	}
}

// TestReceiveBatchMultiFrames: one file's instructions sent as two complete
// 0x02 frames (each a full count-prefixed batch — batches are atomic and must
// not be split across frames).
func TestReceiveBatchMultiFrames(t *testing.T) {
	dir := t.TempDir()
	old, newData := makeOldNew(200000, 7)
	path := filepath.Join(dir, "m.bin")
	if err := os.WriteFile(path, old, 0o644); err != nil {
		t.Fatal(err)
	}

	// Build the full result list, then split it into two batches.
	blockSize := delta.CalculateBlockSize(int64(len(old)))
	sig := delta.GenerateSignature(old, blockSize, delta.GetDefault())
	eng, err := delta.NewMatchEngine(blockSize, delta.GetDefault())
	if err != nil {
		t.Fatal(err)
	}
	eng.LoadSignature(sig)
	var results []delta.MatchResult
	for _, mr := range eng.Search(newData) {
		cp := mr
		if mr.IsLiteral {
			cp.Data = make([]byte, len(mr.Data))
			copy(cp.Data, mr.Data)
		}
		results = append(results, cp)
	}
	mid := len(results) / 2
	encode := func(rs []delta.MatchResult) []byte {
		var buf bytes.Buffer
		if err := delta.WireEncodeInstructions(&buf, rs); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}

	var input bytes.Buffer
	input.Write(batchFrame(t, frameBeginFile, []byte(path)))
	input.Write(batchFrame(t, frameInstr, encode(results[:mid])))
	input.Write(batchFrame(t, frameInstr, encode(results[mid:])))
	input.Write(batchFrame(t, frameEOS, nil))
	input.Write(batchFrame(t, frameEndBatch, nil))

	var out bytes.Buffer
	if err := receiveBatch(bytes.NewReader(input.Bytes()), &out, delta.GetDefault(), true); err != nil {
		t.Fatalf("receiveBatch: %v", err)
	}
	fr := &frameReader{r: bytes.NewReader(out.Bytes())}
	if typ, _, err := fr.read(); err != nil || typ != frameSig {
		t.Fatalf("expected sig, got 0x%02x err=%v", typ, err)
	}
	if typ, payload, err := fr.read(); err != nil || typ != frameResult || len(payload) != 1 || payload[0] != 0 {
		t.Fatalf("expected ok, got 0x%02x %v err=%v", typ, payload, err)
	}
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, newData) {
		t.Fatal("multi-frame reconstruction mismatch")
	}
}
