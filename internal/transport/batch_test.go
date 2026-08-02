package transport

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	delta "github.com/henryborner/go-rsync"
)

// =========================================================================
// Batch-capable mock transport — 模拟支持 receive-batch 的远端 agent
// =========================================================================

// batchMockTransport embeds mockTransport (file store + legacy receive) and
// overrides Exec to speak the batch frame protocol and identify probing.
type batchMockTransport struct {
	*mockTransport
	batchCapable bool // identify advertises :receive-batch / identify 是否宣告批量能力
	killAfter    int  // per-session: kill the batch session after N begin-file frames (0 = never)

	mu        sync.Mutex
	execCmds  []string
	processed map[string]int // remote path → transfer count (exactly-once 断言)
	batchMsgs []string       // batchFile outcomes, for debugging
}

func newBatchMockTransport() *batchMockTransport {
	return &batchMockTransport{
		mockTransport: newMockTransport(),
		processed:     make(map[string]int),
	}
}

func (m *batchMockTransport) countExec(substr string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, c := range m.execCmds {
		if strings.Contains(c, substr) {
			n++
		}
	}
	return n
}

func (m *batchMockTransport) Exec(cmd string) (*RemoteCmd, error) {
	if m.execErrors {
		return nil, fmt.Errorf("mock exec failure")
	}
	m.mu.Lock()
	m.execCmds = append(m.execCmds, cmd)
	m.mu.Unlock()

	switch {
	case strings.Contains(cmd, "identify"):
		return m.execIdentify()
	case strings.Contains(cmd, "receive-batch"):
		return m.execBatch()
	default:
		// legacy per-file receive
		return m.mockTransport.Exec(cmd)
	}
}

func (m *batchMockTransport) execIdentify() (*RemoteCmd, error) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	go func() {
		defer stdinR.Close()
		defer stdoutW.Close()
		defer stderrW.Close()
		// Real agent format (5 fields): id:version:os/arch:algos:caps
		if m.batchCapable {
			fmt.Fprintf(stdoutW, "SHuTtL3_AgEnT_lD:0.1.5.22:linux/amd64:md5:receive-batch\n")
		} else {
			fmt.Fprintf(stdoutW, "SHuTtL3_AgEnT_lD:0.1.5.21:linux/amd64:md5\n")
		}
	}()
	return newMockRemoteCmd(stdinW, stdoutR, stderrR), nil
}

// execBatch runs an in-process agent: begin-file → sig → instr frames → EOS
// → (verify) → result, then end-batch → summary.
func (m *batchMockTransport) execBatch() (*RemoteCmd, error) {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()

	go func() {
		defer stdinR.Close()
		defer stdoutW.Close()
		defer stderrW.Close()
		fr := &testFrameReader{r: stdinR}
		for {
			typ, payload, err := fr.read()
			if err != nil {
				m.mu.Lock()
				m.batchMsgs = append(m.batchMsgs, fmt.Sprintf("loop read err: %v", err))
				m.mu.Unlock()
				fmt.Fprintf(stderrW, "batch: %v", err)
				return
			}
			switch typ {
			case frameBeginFile:
				path := string(payload)

				m.mu.Lock()
				kill := false
				if m.killAfter > 0 {
					m.killAfter--
					// Kill on the N-th begin-file (0 = never): decrement, then
					// kill when the counter hits 0.
					kill = m.killAfter == 0
				}
				m.mu.Unlock()
				if kill {
					// Simulate agent death mid-session: close stdout while the
					// client waits for the signature frame.
					stdoutW.Close()
					return
				}

				msg := m.batchFile(path, fr, stdoutW, stderrW)
				m.mu.Lock()
				m.batchMsgs = append(m.batchMsgs, path+" -> "+msg)
				m.mu.Unlock()
				if msg == "" {
					writeFrame(stdoutW, frameResult, []byte{0})
				} else {
					writeFrame(stdoutW, frameResult, append([]byte{1}, []byte(msg)...))
				}
			case frameEndBatch:
				writeFrame(stdoutW, frameSummary, []byte{0, 0, 0, 0})
				return
			default:
				m.mu.Lock()
				m.batchMsgs = append(m.batchMsgs, fmt.Sprintf("unexpected frame 0x%02x", typ))
				m.mu.Unlock()
				fmt.Fprintf(stderrW, "batch: unexpected frame 0x%02x", typ)
				return
			}
		}
	}()

	return newMockRemoteCmd(stdinW, stdoutR, stderrR), nil
}

// batchFile reconstructs one file. Returns "" on success or an error message.
func (m *batchMockTransport) batchFile(path string, fr *testFrameReader, stdoutW io.Writer, stderrW io.Writer) string {
	m.mu.Lock()
	oldData, exists := m.files[path]
	m.mu.Unlock()

	blockSize := int32(700)
	if exists && len(oldData) > 0 {
		blockSize = delta.CalculateBlockSize(int64(len(oldData)))
	}
	sig := delta.GenerateSignature(oldData, blockSize, delta.GetDefault())
	var sigBuf bytes.Buffer
	if err := delta.WireEncodeSignature(&sigBuf, sig); err != nil {
		return fmt.Sprintf("encode sig: %v", err)
	}
	if err := writeFrame(stdoutW, frameSig, sigBuf.Bytes()); err != nil {
		return fmt.Sprintf("write sig frame: %v", err)
	}

	var frames [][]byte
	for {
		typ, payload, err := fr.read()
		if err != nil {
			return fmt.Sprintf("read instr: %v", err)
		}
		if typ == frameEOS {
			break
		}
		if typ == frameInstr {
			frames = append(frames, payload)
			continue
		}
		return fmt.Sprintf("unexpected frame 0x%02x in instr stream", typ)
	}

	blockLens := make([]int32, len(sig.BlockSums))
	for i := range sig.BlockSums {
		blockLens[i] = sig.BlockSums[i].Length
	}
	recon, err := delta.NewReconstructor(oldData, sig.BlockSize, delta.GetDefault(), blockLens)
	if err != nil {
		return fmt.Sprintf("recon: %v", err)
	}
	var result bytes.Buffer
	// Each 0x02 frame carries one complete count-prefixed batch; decode per
	// frame (no 0-count terminator exists on the wire).
	for _, f := range frames {
		if err := delta.DecodeInstructionsStream(bytes.NewReader(f), func(mr delta.MatchResult) error {
			return recon.WriteInstruction(&result, mr)
		}); err != nil {
			return fmt.Sprintf("decode: %v", err)
		}
	}

	// The client always sends a verify frame after EOS: [1][sha256] to check
	// or [0] to skip.
	if typ, payload, err := fr.read(); err == nil && typ == frameVerify {
		if len(payload) == 33 && payload[0] == 1 {
			var want [32]byte
			copy(want[:], payload[1:])
			got := sha256.Sum256(result.Bytes())
			if got != want {
				return "verify mismatch"
			}
		} else if !(len(payload) == 1 && payload[0] == 0) {
			return "malformed verify frame"
		}
	} else if err == nil {
		fr.unread(typ, payload)
	}

	m.mu.Lock()
	m.files[path] = result.Bytes()
	m.processed[path]++
	m.mu.Unlock()
	return ""
}

// testFrameReader adds single-frame lookahead over the frame stream.
type testFrameReader struct {
	r    io.Reader
	typ  byte
	data []byte
	has  bool
}

func (f *testFrameReader) read() (byte, []byte, error) {
	if f.has {
		f.has = false
		return f.typ, f.data, nil
	}
	return readFrame(f.r)
}

func (f *testFrameReader) unread(typ byte, data []byte) {
	f.typ, f.data, f.has = typ, data, true
}

// =========================================================================
// 测试
// =========================================================================

// buildSyncFixture writes `n` local files (deterministic content) and plants
// older remote versions (slightly shorter) so every file goes through delta.
func buildSyncFixture(t *testing.T, n int) (string, []string) {
	t.Helper()
	dir := t.TempDir()
	names := make([]string, n)
	for i := 0; i < n; i++ {
		name := fmt.Sprintf("file_%02d.dat", i)
		data := make([]byte, 50000+i*1000)
		for j := range data {
			data[j] = byte((j*7 + 13) % 251)
		}
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
		names[i] = name
	}
	return dir, names
}

func plantRemoteOld(mock *batchMockTransport, dir, target string, names []string, trunc int) {
	for _, name := range names {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			panic(err)
		}
		old := data[:len(data)-trunc]
		// Flip a prefix so content differs even if sizes were equal.
		for j := 0; j < 2000 && j < len(old); j++ {
			old[j] ^= 0xFF
		}
		mock.files[target+"/"+name] = old
	}
}

func assertRemoteMatches(t *testing.T, mock *batchMockTransport, dir, target string, names []string, viaBatch map[string]bool) {
	t.Helper()
	for _, name := range names {
		want, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		got, ok := mock.files[target+"/"+name]
		if !ok {
			t.Errorf("remote %s missing", name)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("remote %s content mismatch: got %d bytes, want %d", name, len(got), len(want))
		}
		if viaBatch[name] {
			if c := mock.processed[target+"/"+name]; c != 1 {
				t.Errorf("exactly-once violated for %s: processed %d times", name, c)
			}
		}
	}
}

// TestSyncUsesBatchSession: batch-capable agent → one persistent receive-batch
// exec per worker, no legacy receive execs, correct contents, exactly-once.
func TestSyncUsesBatchSession(t *testing.T) {
	mock := newBatchMockTransport()
	mock.batchCapable = true
	dir, names := buildSyncFixture(t, 5)
	target := "/remote"
	plantRemoteOld(mock, dir, target, names, 777)

	stats, err := NewSyncEngine(mock).Sync(SyncOptions{
		Source: dir, Target: target, Flat: true, Workers: 1,
	})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if stats.UpdatedFiles != len(names) {
		t.Errorf("expected %d updated, got %d", len(names), stats.UpdatedFiles)
	}
	if n := mock.countExec("receive-batch"); n != 1 {
		t.Errorf("expected exactly 1 receive-batch exec (Workers=1), got %d", n)
	}
	if n := mock.countExec("shuttle receive --algo"); n != 0 {
		t.Errorf("expected 0 legacy receive execs, got %d", n)
	}
	if n := mock.countExec("identify"); n != 1 {
		t.Errorf("expected 1 identify probe, got %d", n)
	}
	assertRemoteMatches(t, mock, dir, target, names, map[string]bool{
		"file_00.dat": true, "file_01.dat": true, "file_02.dat": true,
		"file_03.dat": true, "file_04.dat": true,
	})
}

// TestSyncBatchWorkers: with Workers=4 there are 4 persistent sessions; every
// file is still transferred exactly once.
func TestSyncBatchWorkers(t *testing.T) {
	mock := newBatchMockTransport()
	mock.batchCapable = true
	dir, names := buildSyncFixture(t, 9)
	target := "/remote"
	plantRemoteOld(mock, dir, target, names, 513)

	stats, err := NewSyncEngine(mock).Sync(SyncOptions{
		Source: dir, Target: target, Flat: true, Workers: 4,
	})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if stats.UpdatedFiles != len(names) {
		t.Errorf("expected %d updated, got %d", len(names), stats.UpdatedFiles)
	}
	if n := mock.countExec("receive-batch"); n != 4 {
		t.Errorf("expected 4 receive-batch execs (one per worker), got %d", n)
	}
	if n := mock.countExec("shuttle receive --algo"); n != 0 {
		t.Errorf("expected 0 legacy receive execs, got %d", n)
	}
	allBatch := map[string]bool{}
	for _, name := range names {
		allBatch[name] = true
	}
	assertRemoteMatches(t, mock, dir, target, names, allBatch)
}

// TestSyncFallsBackLegacy: agent without the capability → per-file receive.
func TestSyncFallsBackLegacy(t *testing.T) {
	mock := newBatchMockTransport()
	mock.batchCapable = false
	dir, names := buildSyncFixture(t, 4)
	target := "/remote"
	plantRemoteOld(mock, dir, target, names, 601)

	stats, err := NewSyncEngine(mock).Sync(SyncOptions{
		Source: dir, Target: target, Flat: true, Workers: 2,
	})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if stats.UpdatedFiles != len(names) {
		t.Errorf("expected %d updated, got %d", len(names), stats.UpdatedFiles)
	}
	if n := mock.countExec("receive-batch"); n != 0 {
		t.Errorf("expected 0 receive-batch execs, got %d", n)
	}
	if n := mock.countExec("shuttle receive --algo"); n != len(names) {
		t.Errorf("expected %d legacy receive execs, got %d", len(names), n)
	}
	assertRemoteMatches(t, mock, dir, target, names, nil)
}

// TestSyncBatchBrokenFallsBack: the batch session dies mid-stream (after 2
// files) → affected + remaining files retried per-file, all correct.
func TestSyncBatchBrokenFallsBack(t *testing.T) {
	mock := newBatchMockTransport()
	mock.batchCapable = true
	mock.killAfter = 3 // session survives 2 files, dies on the 3rd begin-file
	dir, names := buildSyncFixture(t, 5)
	target := "/remote"
	plantRemoteOld(mock, dir, target, names, 331)

	stats, err := NewSyncEngine(mock).Sync(SyncOptions{
		Source: dir, Target: target, Flat: true, Workers: 1,
	})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if stats.UpdatedFiles != len(names) {
		t.Errorf("expected %d updated, got %d", len(names), stats.UpdatedFiles)
	}
	if n := mock.countExec("receive-batch"); n != 1 {
		t.Errorf("expected 1 receive-batch exec, got %d", n)
	}
	if n := mock.countExec("shuttle receive --algo"); n != len(names)-2 {
		t.Errorf("expected %d legacy receive execs, got %d", len(names)-2, n)
	}
	assertRemoteMatches(t, mock, dir, target, names, map[string]bool{
		"file_00.dat": true, "file_01.dat": true,
	})
}

// TestSyncBatchVerify: Verify option sends a 0x03 frame; a mock that detects a
// hash mismatch reports a per-file error (session abandoned → fallback).
func TestSyncBatchVerify(t *testing.T) {
	mock := newBatchMockTransport()
	mock.batchCapable = true
	dir, names := buildSyncFixture(t, 3)
	target := "/remote"
	plantRemoteOld(mock, dir, target, names, 199)

	stats, err := NewSyncEngine(mock).Sync(SyncOptions{
		Source: dir, Target: target, Flat: true, Workers: 1, Verify: true,
	})
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if stats.UpdatedFiles != len(names) {
		t.Errorf("expected %d updated, got %d", len(names), stats.UpdatedFiles)
	}
	allBatch := map[string]bool{}
	for _, name := range names {
		allBatch[name] = true
	}
	assertRemoteMatches(t, mock, dir, target, names, allBatch)
}

// TestAgentSupportsBatch checks the probe against identify outputs.
func TestAgentSupportsBatch(t *testing.T) {
	capable := newBatchMockTransport()
	capable.batchCapable = true
	eng := NewSyncEngine(capable)
	if !eng.agentSupportsBatch() {
		t.Error("expected batch support when identify advertises :receive-batch")
	}

	legacy := newBatchMockTransport()
	legacy.batchCapable = false
	eng2 := NewSyncEngine(legacy)
	if eng2.agentSupportsBatch() {
		t.Error("expected no batch support for a legacy identify string")
	}
}

// silence unused-import guard for time (kept for parity with fixture style).
var _ = time.Now
