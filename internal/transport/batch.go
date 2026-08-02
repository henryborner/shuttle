package transport

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	delta "github.com/henryborner/go-rsync"
)

// ── receive-batch client: one SSH exec handles many delta files ──────────
// Mirrors the agent's frame protocol (cmd/shuttle_agent/main.go). Each file is
// processed as begin → signature → instruction frames → EOS (+verify) → result.
// A single Exec session is reused across files, eliminating per-file process
// spawn + signature RTT. Falls back to the legacy per-file `receive` path when
// the agent lacks the capability or the session breaks.
//
// 帧协议与 agent 一致。单个 Exec 会话跨多个文件复用，消除每文件进程启动 +
// 签名 RTT。agent 无能力或会话中断时回退旧 per-file receive。

const (
	frameBeginFile = 0x01
	frameInstr     = 0x02
	frameEOS       = 0x00
	frameVerify    = 0x03
	frameEndBatch  = 0xFF

	frameSig     = 0x11
	frameResult  = 0x12
	frameSummary = 0x13

	maxFrameLen = 64 << 20
)

func writeFrame(w io.Writer, typ byte, payload []byte) error {
	var hdr [5]byte
	hdr[0] = typ
	binary.BigEndian.PutUint32(hdr[1:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	if len(payload) > 0 {
		if _, err := w.Write(payload); err != nil {
			return err
		}
	}
	return nil
}

func readFrame(r io.Reader) (byte, []byte, error) {
	var hdr [5]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	n := binary.BigEndian.Uint32(hdr[1:])
	if n > maxFrameLen {
		return 0, nil, fmt.Errorf("frame payload %d exceeds max %d", n, maxFrameLen)
	}
	payload := make([]byte, n)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return hdr[0], payload, nil
}

// batchDeltaSession wraps one `shuttle receive-batch` exec, reused across many
// files. dead is set when the stream can no longer be used (sendFrame/read
// failure or a remote error); the caller then falls back to per-file receive.
type batchDeltaSession struct {
	e      *SyncEngine
	cmd    *RemoteCmd
	stdin  io.WriteCloser
	stdout io.Reader
	algo   string
	verify bool
	dead   bool
}

// openBatchSession execs `shuttle receive-batch` once. Returns a reusable
// session, or nil+err if the command cannot be started.
func (e *SyncEngine) openBatchSession(checksum, verify bool) (*batchDeltaSession, error) {
	algo := delta.GetDefault()
	cmdStr := "shuttle receive-batch --algo '" + algo + "'"
	if checksum {
		cmdStr = "shuttle receive-batch --algo '" + algo + "' --no-cache"
	}
	cmd, err := e.transport.Exec(cmdStr)
	if err != nil {
		return nil, err
	}
	return &batchDeltaSession{
		e:      e,
		cmd:    cmd,
		stdin:  cmd.Stdin,
		stdout: cmd.Stdout,
		algo:   algo,
		verify: verify,
	}, nil
}

func (s *batchDeltaSession) write(typ byte, payload []byte) error {
	if s.dead {
		return fmt.Errorf("batch session is dead")
	}
	if err := writeFrame(s.stdin, typ, payload); err != nil {
		s.dead = true
		return err
	}
	return nil
}

func (s *batchDeltaSession) read() (byte, []byte, error) {
	if s.dead {
		return 0, nil, fmt.Errorf("batch session is dead")
	}
	typ, payload, err := readFrame(s.stdout)
	if err != nil {
		s.dead = true
		return 0, nil, err
	}
	return typ, payload, nil
}

// sendFile performs one delta transfer through the batch session. On error the
// session is marked dead so the caller retries via the per-file path.
func (s *batchDeltaSession) sendFile(info LocalFileInfo, remotePath string) (sentBytes, savedBytes int64, err error) {
	// 1. begin file.
	if err := s.write(frameBeginFile, []byte(remotePath)); err != nil {
		return 0, 0, err
	}
	// 2. signature (or an agent-side error result frame).
	typ, payload, err := s.read()
	if err != nil {
		return 0, 0, err
	}
	if typ == frameResult {
		// Agent failed before producing a signature (e.g. missing file).
		s.dead = true
		return 0, 0, fmt.Errorf("remote: %s", string(payload[1:]))
	}
	if typ != frameSig {
		s.dead = true
		return 0, 0, fmt.Errorf("unexpected frame 0x%02x, want signature", typ)
	}
	sig, err := delta.WireDecodeSignature(bytes.NewReader(payload))
	if err != nil {
		s.dead = true
		return 0, 0, fmt.Errorf("signature decode failed: %w", err)
	}

	// 3. open local file, stream match + send instruction frames.
	f, err := os.Open(info.Path)
	if err != nil {
		s.dead = true
		return 0, 0, err
	}
	defer f.Close()

	eng, err := delta.NewMatchEngine(sig.BlockSize, s.algo)
	if err != nil {
		s.dead = true
		return 0, 0, fmt.Errorf("create match engine: %w", err)
	}
	eng.LoadSignature(sig)

	const batchSize = 256
	batch := make([]delta.MatchResult, 0, batchSize)
	var sent int64
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		var buf bytes.Buffer
		if err := delta.WireEncodeInstructions(&buf, batch); err != nil {
			return err
		}
		payload := buf.Bytes()
		if err := s.write(frameInstr, payload); err != nil {
			return err
		}
		sent += int64(len(payload))
		batch = batch[:0]
		return nil
	}

	var lastProgress int64
	err = eng.SearchReader(f, info.Size, func(mr delta.MatchResult) error {
		if mr.Offset > lastProgress {
			lastProgress = mr.Offset
			s.e.hook.OnFileProgress(info.Path, lastProgress, info.Size)
		}
		cp := mr
		if mr.IsLiteral {
			cp.Data = make([]byte, len(mr.Data))
			copy(cp.Data, mr.Data)
		}
		batch = append(batch, cp)
		if len(batch) >= batchSize {
			return flush()
		}
		return nil
	})
	if err != nil {
		s.dead = true
		return 0, 0, err
	}
	if err := flush(); err != nil {
		s.dead = true
		return 0, 0, err
	}

	// 4. EOS, then ALWAYS a verify frame (the agent blocks on this read after
	// EOS): [1][sha256] when verifying, [0] as an explicit "skip" marker.
	if err := s.write(frameEOS, nil); err != nil {
		return 0, 0, err
	}
	var vPayload []byte
	if s.verify {
		fileHash, hashErr := computeFileSHA256(info.Path)
		if hashErr != nil {
			s.e.warn("verify: cannot hash local file %s: %v", filepath.Base(info.Path), hashErr)
			vPayload = []byte{0}
		} else {
			vPayload = append([]byte{1}, fileHash[:]...)
			sent += int64(1 + len(fileHash))
		}
	} else {
		vPayload = []byte{0}
	}
	if err := s.write(frameVerify, vPayload); err != nil {
		return 0, 0, err
	}

	// 5. per-file result.
	typ, payload, err = s.read()
	if err != nil {
		return 0, 0, err
	}
	if typ != frameResult {
		s.dead = true
		return 0, 0, fmt.Errorf("unexpected frame 0x%02x, want result", typ)
	}
	if len(payload) > 0 && payload[0] == 1 {
		s.dead = true
		return sent, 0, fmt.Errorf("remote: %s", string(payload[1:]))
	}

	// 6. mtime (best-effort, mirrors legacy path).
	if err := s.e.transport.SetModTime(remotePath, info.ModTime); err != nil {
		s.e.warn("delta: set mtime for %s: %v", filepath.Base(info.Path), err)
	}

	return sent, info.Size - eng.LiteralBytes, nil
}

// close sends the end-of-batch marker, reads the summary, and releases the
// session. Called when the session is healthy.
func (s *batchDeltaSession) close() error {
	if s.dead {
		s.abort()
		return nil
	}
	if err := s.write(frameEndBatch, nil); err != nil {
		s.abort()
		return err
	}
	// Drain the summary frame (best-effort; the important work is done).
	if _, _, err := s.read(); err != nil {
		s.abort()
		return err
	}
	if err := s.cmd.Close(); err != nil {
		return err
	}
	return nil
}

// abort releases the session without a clean end-of-batch handshake. Closing
// stdin first signals EOF so the remote agent unwinds and exits; Close() then
// drains stderr, waits, and releases the SSH session.
func (s *batchDeltaSession) abort() {
	if s.stdin != nil {
		_ = s.stdin.Close()
	}
	if s.cmd != nil {
		_ = s.cmd.Close()
	}
}

// agentSupportsBatch probes the remote agent's identify output for the
// receive-batch capability. False on any error (falls back to per-file).
func (e *SyncEngine) agentSupportsBatch() bool {
	cmd, err := e.transport.Exec("shuttle identify")
	if err != nil {
		return false
	}
	out, _ := io.ReadAll(cmd.Stdout)
	_ = cmd.Close()
	return strings.Contains(string(out), ":receive-batch")
}

// deltaPhaseBatch runs delta transfers with persistent workers, each owning
// one batch session, pulling jobs from a shared channel (exactly-once
// delivery). A broken session falls back to the per-file path for the
// affected file and its remaining jobs.
func (e *SyncEngine) deltaPhaseBatch(deltaJobs []deltaJob, workers int, checksum, verify bool, stats *SyncStats) {
	jobs := make(chan deltaJob)
	resultCh := make(chan deltaResult, len(deltaJobs))

	worker := func() {
		sess, err := e.openBatchSession(checksum, verify)
		for job := range jobs {
			start := time.Now()
			e.hook.OnFileStart(job.relPath, job.lf.Size)
			var sent, saved int64
			var fe error
			if err != nil {
				// Batch session unavailable → per-file for this and the rest.
				sent, saved, fe = e.uploadFileDelta(job.lf, job.remotePath, checksum, verify)
			} else {
				sent, saved, fe = sess.sendFile(job.lf, job.remotePath)
				if fe != nil {
					// Session broke — retry this file per-file, then the rest too.
					sess.abort()
					e.warn("delta: batch session broken on %s (%v); falling back to per-file", job.relPath, fe)
					err = fmt.Errorf("batch session failed")
					sent, saved, fe = e.uploadFileDelta(job.lf, job.remotePath, checksum, verify)
				}
			}
			resultCh <- deltaResult{job, sent, saved, start, fe}
		}
		if err == nil && sess != nil {
			_ = sess.close()
		}
	}

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker()
		}()
	}
	// Send jobs from a separate goroutine. If this (main) goroutine sent them
	// synchronously, each send blocks until a worker accepts the job, so the
	// consume loop below — and every OnFileDone — would only start once all
	// jobs are handed out (roughly one file-duration before the last one
	// finishes). That delays every status line to the end, exactly the bug
	// reported on a TTY: bars finish but no "UPD/Δ file" line appears until
	// the batch is nearly done.
	// 发送放在独立 goroutine：若主 goroutine 同步发送，每个发送都要等 worker
	// 接收，下方消费循环（含所有 OnFileDone）要等全部 job 派发完才开始——
	// 状态行会被推迟到接近结束才一次性输出（TTY 上报的 bug）。
	go func() {
		for _, dj := range deltaJobs {
			jobs <- dj
		}
		close(jobs)
	}()

	// Consume results as they arrive so status lines (OnFileDone) print
	// immediately per file, matching deltaPhaseLegacy / uploadPhase.
	// 边收边处理：每个文件完成立即输出状态行（与 legacy/uploadPhase 一致）。
	for i := 0; i < len(deltaJobs); i++ {
		r := <-resultCh
		stats.UpdatedFiles++
		stats.DeltaBytes += r.job.lf.Size
		stats.SentBytes += r.sent
		stats.DeltaSaved += r.saved
		if r.saved > 0 {
			stats.DeltaFiles++
		}
		e.hook.OnFileDone(FileEvent{
			RelPath: r.job.relPath, RemotePath: r.job.remotePath,
			FileSize: r.job.lf.Size, BytesSent: r.sent,
			IsUpdated: true, IsDelta: r.saved > 0, DeltaSaved: r.saved,
			StartTime: r.start, Duration: time.Since(r.start),
			Error: r.err,
		})
		if r.err != nil {
			stats.Errors = append(stats.Errors, r.err)
		}
	}
	// Workers may still be finishing (closing their batch sessions); wait for
	// them so the sessions are released before this phase returns.
	// worker 可能仍在收尾（关闭 batch 会话），等待它们释放会话。
	wg.Wait()
}
