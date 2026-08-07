// shuttle_agent — minimal remote agent binary for Linux servers.
// Only imports go-rsync + stdlib. No cobra, no TUI, no SSH client, no config.
// shuttle_agent — 远端 Linux 服务器的最小 agent 二进制。
// 仅导入 go-rsync + 标准库。无 cobra、无 TUI、无 SSH 客户端、无配置。
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	delta "github.com/henryborner/go-rsync"
)

const Version = "0.1.6.1"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: shuttle receive|receive-batch|identify|version")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "receive":
		runReceive()
	case "receive-batch":
		runReceiveBatch()
	case "identify":
		runIdentify()
	case "version":
		runVersion()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

func runVersion() {
	fmt.Printf("Shuttle v%s\n", Version)
	fmt.Printf("  Go:     %s\n", runtime.Version())
	fmt.Printf("  OS:     %s\n", runtime.GOOS)
	fmt.Printf("  Arch:   %s\n", runtime.GOARCH)
	fmt.Printf("  Strong: %s\n", delta.GetDefault())
	fmt.Printf("  Algos:  %s\n", strings.Join(delta.ListAlgos(), ", "))
}

func runIdentify() {
	// 4th colon-field = capabilities (comma-separated). Old clients only check
	// the "SHuTtL3_AgEnT_lD:" prefix (agent.Check), so this is backward-compatible.
	fmt.Printf("SHuTtL3_AgEnT_lD:%s:%s/%s:%s:%s\n",
		Version, runtime.GOOS, runtime.GOARCH,
		strings.Join(delta.ListAlgos(), ","), "receive-batch")
}

// ── receive command (copied from cmd/shuttle/receive.go, decoupled from cobra) ──

func runReceive() {
	// Parse flags manually: shuttle receive [--algo md5] [--cache] [--no-cache] <filepath>
	//
	// The signature cache is DISABLED by default: single-client setups get
	// ~0% hits because SetModTime rewrites the remote mtime on every sync and
	// mtime is part of the cache key, so the key never matches the previous
	// entry. Pass --cache to enable it (useful for multi-client servers where
	// several clients sync the same remote files). --no-cache always wins.
	// 签名缓存默认禁用：单客户端场景命中率≈0（每次同步 SetModTime 都会改写远程
	// mtime，而 mtime 是缓存键的一部分，键永远对不上上次的条目）。多客户端共享
	// 同一远程文件的场景可传 --cache 启用。--no-cache 始终优先（强制禁用）。
	fs := flag.NewFlagSet("receive", flag.ExitOnError)
	algo := fs.String("algo", "md5", "strong checksum algorithm")
	cache := fs.Bool("cache", false, "enable signature cache (default off)")
	noCache := fs.Bool("no-cache", false, "skip signature cache (overrides --cache)")
	fs.Parse(os.Args[2:])

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "RECEIVER ERROR: missing file path")
		os.Exit(1)
	}
	filePath := fs.Arg(0)
	useCache := *cache && !*noCache
	if err := receiveFile(filePath, os.Stdin, os.Stdout, *algo, !useCache); err != nil {
		fmt.Fprintf(os.Stderr, "RECEIVER ERROR: %v\n", err)
		os.Exit(1)
	}
}

// receiveFile performs the delta receive for one file: writes the block
// signature to stdout, then reconstructs the new file from the instruction
// stream on stdin into an atomic temp+rename. Split out of runReceive so the
// reconstruction pipeline (including the buffered writer) is unit-testable.
// receiveFile 对单个文件执行 delta receive：向 stdout 输出块签名，然后从
// stdin 的指令流重建新文件（临时文件 + 原子 rename）。从 runReceive 拆分出来
// 以便对重建流水线（含缓冲写入）做单元测试。
func receiveFile(filePath string, stdin io.Reader, stdout io.Writer, algo string, noCache bool) error {
	// 1. Open local old file (stream read signature).
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("读取文件失败: %w", err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return fmt.Errorf("stat 失败: %w", err)
	}
	fileSize := fi.Size()

	// 2. Generate or load cached block signatures.
	blockSize := delta.CalculateBlockSize(fileSize)
	var sig *delta.Signature
	var sigWire []byte

	if !noCache {
		cached, _ := cacheLoad(filePath, fi, blockSize, algo)
		if cached != nil {
			s, err := delta.WireDecodeSignature(bytes.NewReader(cached))
			if err == nil {
				sig = s
				sigWire = cached
			}
		}
	}
	if sig == nil {
		var gsrErr error
		sig, gsrErr = delta.GenerateSignatureReader(f, fileSize, blockSize, algo)
		if gsrErr != nil {
			return fmt.Errorf("generate signature failed: %w", gsrErr)
		}
		var buf bytes.Buffer
		if err := delta.WireEncodeSignature(&buf, sig); err != nil {
			return fmt.Errorf("encode signature failed: %w", err)
		}
		sigWire = buf.Bytes()
		if !noCache {
			if err := cacheSave(filePath, fi, blockSize, algo, sigWire); err != nil {
				fmt.Fprintf(os.Stderr, "RECEIVER WARNING: signature cache save failed: %v\n", err)
			}
		}
	}

	// 3. Send signature to stdout.
	if _, err := stdout.Write(sigWire); err != nil {
		return fmt.Errorf("send signature failed: %w", err)
	}

	// Close the signature-read handle so the final rename can replace the file
	// on Windows (an open handle there causes "Access is denied"). Linux is
	// unaffected but this is harmless.
	// 关闭签名读取句柄，否则 Windows 上 rename 覆盖仍被打开的句柄会报
	// "Access is denied"。Linux 不受影响，此举无害。
	if err := f.Close(); err != nil {
		return fmt.Errorf("close signature handle: %w", err)
	}

	// 4-6. Reconstruct via the shared core (single-file: raw instruction
	// stream on stdin, verify flag+hash after the count=0 EOS). An abandoned
	// stream (EOF without EOS) is not an error for the single-file path.
	if err := receiveFileCore(filePath, sig, blockSize, algo,
		func(fn func(delta.MatchResult) error) error {
			return delta.DecodeInstructionsStreamAll(stdin, fn)
		},
		func() (*[32]byte, error) {
			flag := make([]byte, 1)
			if n, _ := io.ReadFull(stdin, flag); n == 1 && flag[0] == 0x01 {
				var h [32]byte
				if _, err := io.ReadFull(stdin, h[:]); err != nil {
					return nil, fmt.Errorf("read verify hash failed: %w", err)
				}
				return &h, nil
			}
			return nil, nil
		}); err != nil && err != errAbandoned {
		return err
	}
	return nil
}

// errAbandoned signals that the instruction stream ended (EOF) without an EOS
// marker — the sender closed the connection. The temp file is discarded.
var errAbandoned = errors.New("instruction stream abandoned (EOF without EOS)")

// receiveFileCore reconstructs filePath from sig using the instruction source
// provided by readInstructions, writes the result to temp + atomic rename, and
// optionally verifies the SHA256 returned by readVerify. Shared by the
// single-file (receive) and batch (receive-batch) paths.
func receiveFileCore(filePath string, sig *delta.Signature, blockSize int32, algo string,
	readInstructions func(fn func(delta.MatchResult) error) error,
	readVerify func() (*[32]byte, error)) error {

	tmpPath := filePath + ".shuttle_tmp"
	out, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %w", err)
	}
	cleanup := func() {
		out.Close()
		os.Remove(tmpPath)
	}
	succeeded := false
	defer func() {
		if !succeeded {
			cleanup()
		}
	}()

	// Buffered writer: a delta with many small instructions (e.g. scattered
	// literal chunks) would otherwise issue one syscall per instruction.
	// Flush once after the instruction stream completes.
	// 带缓冲写入：指令流含大量小块（如分散的 literal 片段）时，避免每条指令
	// 一次系统调用。指令流结束后统一 flush 一次。
	bw := bufio.NewWriterSize(out, 64<<10)

	// Read basis file for reconstruction (prefer mmap, fallback ReadFile).
	oldData, closer, err := mmapReadOnly(filePath)
	if err != nil {
		oldData, err = os.ReadFile(filePath)
		if err != nil {
			return fmt.Errorf("读取文件失败: %w", err)
		}
	}
	if closer != nil {
		defer closer()
	}

	blockLens := make([]int32, len(sig.BlockSums))
	for i, bs := range sig.BlockSums {
		blockLens[i] = bs.Length
	}
	recon, err := delta.NewReconstructor(oldData, blockSize, algo, blockLens)
	if err != nil {
		return fmt.Errorf("create reconstructor failed: %w", err)
	}

	err = readInstructions(func(inst delta.MatchResult) error {
		return recon.WriteInstruction(bw, inst)
	})
	if err != nil {
		if isEOF(err) {
			return errAbandoned
		}
		return fmt.Errorf("流式重建失败: %w", err)
	}

	// Flush buffered writes before verify/rename.
	// 先 flush 缓冲，再校验/重命名。
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("flush 失败: %w", err)
	}

	// Optional verify: read the SHA256 trailer after EOS.
	var expectedHash *[32]byte
	if expectedHash, err = readVerify(); err != nil {
		return err
	}

	// Close output file, atomic rename.
	if err := out.Close(); err != nil {
		return fmt.Errorf("关闭临时文件失败: %w", err)
	}

	if expectedHash != nil {
		data, err := os.ReadFile(tmpPath)
		if err != nil {
			return fmt.Errorf("校验读取临时文件失败: %w", err)
		}
		actual := sha256.Sum256(data)
		if actual != *expectedHash {
			return fmt.Errorf("verify failed — reconstructed file hash mismatch")
		}
	}

	// Release the mmap view before rename: on Windows an active mapping
	// would block replacing the file ("Access is denied"). Linux is
	// unaffected; this is harmless there too.
	// rename 前释放 mmap 视图：Windows 上活跃的映射会阻止替换文件
	// （"Access is denied"）。Linux 不受影响，此举无害。
	if closer != nil {
		closer()
		closer = nil
	}

	if err := os.Rename(tmpPath, filePath); err != nil {
		return fmt.Errorf("替换文件失败: %w", err)
	}
	succeeded = true
	return nil
}

// ── receive-batch: one process handles many files over a framed stream ──
//
// Frame protocol (length-prefixed, big-endian u32 payload length):
//
//	client→agent (stdin):
//	  0x01 + path          begin file <path>
//	  0x02 + instr batch   one wire-encoded instruction batch (count > 0)
//	  0x00                 end of this file's instruction stream (EOS)
//	  0x03 + flag + sha256 optional verify trailer for this file
//	  0xFF                 end of batch
//	agent→client (stdout):
//	  0x11 + sig           wire-encoded signature for the current file
//	  0x12 + status + msg  file result (0 = ok, 1 = error + message)
//	  0x13 + ok(4)+fail(4) batch summary
//
// Each file is independent: an error on one file is reported in its result
// frame and the batch continues. The stream stays in sync because the client
// reads exactly one result frame per file before starting the next one.
//
// 帧协议（长度前缀，u32 大端）。每文件独立：单文件错误在结果帧中上报，
// 批次继续；客户端每文件恰好读一个结果帧，因此流保持同步。

const (
	frameBeginFile = 0x01
	frameInstr     = 0x02
	frameEOS       = 0x00
	frameVerify    = 0x03
	frameEndBatch  = 0xFF

	frameSig     = 0x11
	frameResult  = 0x12
	frameSummary = 0x13

	// Max payload: a 256-batch of 32KB literal chunks is ~8MB; cap well above.
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

// frameReader wraps a stream with single-frame lookahead so the instruction
// reader can "unread" a frame that belongs to the next file.
type frameReader struct {
	r       io.Reader
	typ     byte
	payload []byte
	has     bool
}

func (fr *frameReader) read() (byte, []byte, error) {
	if fr.has {
		fr.has = false
		return fr.typ, fr.payload, nil
	}
	return readFrame(fr.r)
}

func (fr *frameReader) unread(typ byte, payload []byte) {
	fr.typ, fr.payload, fr.has = typ, payload, true
}

func runReceiveBatch() {
	fs := flag.NewFlagSet("receive-batch", flag.ExitOnError)
	algo := fs.String("algo", "md5", "strong checksum algorithm")
	cache := fs.Bool("cache", false, "enable signature cache (default off)")
	noCache := fs.Bool("no-cache", false, "skip signature cache (overrides --cache)")
	fs.Parse(os.Args[2:])
	useCache := *cache && !*noCache
	if err := receiveBatch(os.Stdin, os.Stdout, *algo, !useCache); err != nil {
		fmt.Fprintf(os.Stderr, "RECEIVER ERROR: %v\n", err)
		os.Exit(1)
	}
}

// receiveBatch is the framed multi-file receive loop. Files are processed
// serially in the order sent. On an abandoned stream (EOF without EOS) the
// whole batch aborts — the connection is gone, no result frame can be read.
func receiveBatch(stdin io.Reader, stdout io.Writer, algo string, noCache bool) error {
	fr := &frameReader{r: stdin}
	ok, failed := 0, 0
	for {
		typ, payload, err := fr.read()
		if err != nil {
			return fmt.Errorf("read frame: %w", err)
		}
		switch typ {
		case frameEndBatch:
			var sum [8]byte
			binary.BigEndian.PutUint32(sum[0:4], uint32(ok))
			binary.BigEndian.PutUint32(sum[4:8], uint32(failed))
			return writeFrame(stdout, frameSummary, sum[:])
		case frameBeginFile:
			if len(payload) == 0 {
				failed++
				writeFrame(stdout, frameResult, []byte{1})
				continue
			}
			path := string(payload)
			if err := receiveBatchFile(path, fr, stdout, algo, noCache); err != nil {
				if err == errAbandoned {
					return err
				}
				failed++
				msg := make([]byte, 0, 1+len(err.Error()))
				msg = append(msg, 1)
				msg = append(msg, err.Error()...)
				if wErr := writeFrame(stdout, frameResult, msg); wErr != nil {
					return wErr
				}
			} else {
				ok++
				if wErr := writeFrame(stdout, frameResult, []byte{0}); wErr != nil {
					return wErr
				}
			}
		default:
			return fmt.Errorf("unexpected frame type 0x%02x", typ)
		}
	}
}

// receiveBatchFile handles one file inside a batch: generates (or loads
// cached) signatures, sends the signature frame, then streams the framed
// instruction batches into the shared reconstruction core.
func receiveBatchFile(filePath string, fr *frameReader, stdout io.Writer, algo string, noCache bool) error {
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("读取文件失败: %w", err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return fmt.Errorf("stat 失败: %w", err)
	}
	fileSize := fi.Size()

	blockSize := delta.CalculateBlockSize(fileSize)
	var sig *delta.Signature
	var sigWire []byte

	if !noCache {
		cached, _ := cacheLoad(filePath, fi, blockSize, algo)
		if cached != nil {
			s, err := delta.WireDecodeSignature(bytes.NewReader(cached))
			if err == nil {
				sig = s
				sigWire = cached
			}
		}
	}
	if sig == nil {
		gsr, err := delta.GenerateSignatureReader(f, fileSize, blockSize, algo)
		if err != nil {
			f.Close()
			return fmt.Errorf("generate signature failed: %w", err)
		}
		var buf bytes.Buffer
		if err := delta.WireEncodeSignature(&buf, gsr); err != nil {
			f.Close()
			return fmt.Errorf("encode signature failed: %w", err)
		}
		sigWire = buf.Bytes()
		if !noCache {
			if err := cacheSave(filePath, fi, blockSize, algo, sigWire); err != nil {
				fmt.Fprintf(os.Stderr, "RECEIVER WARNING: signature cache save failed: %v\n", err)
			}
		}
		sig = gsr
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close signature handle: %w", err)
	}

	// Send the signature frame; the client reads it (or an error result frame)
	// before sending instructions.
	if err := writeFrame(stdout, frameSig, sigWire); err != nil {
		return err
	}

	// Read framed instruction batches until EOS, then an optional verify frame.
	return receiveFileCore(filePath, sig, blockSize, algo,
		func(fn func(delta.MatchResult) error) error {
			for {
				typ, payload, err := fr.read()
				if err != nil {
					return err
				}
				switch typ {
				case frameInstr:
					if err := delta.DecodeInstructionsStream(bytes.NewReader(payload), fn); err != nil {
						return err
					}
				case frameEOS:
					return nil
				default:
					return fmt.Errorf("unexpected frame 0x%02x in instruction stream", typ)
				}
			}
		},
		func() (*[32]byte, error) {
			typ, payload, err := fr.read()
			if err != nil {
				return nil, err
			}
			if typ == frameVerify {
				if len(payload) == 1 && payload[0] == 0x00 {
					// Explicit "no verify" marker: client always sends a
					// verify frame after EOS to unblock this read.
					return nil, nil
				}
				if len(payload) == 1+32 && payload[0] == 0x01 {
					h := new([32]byte)
					copy(h[:], payload[1:])
					return h, nil
				}
				return nil, fmt.Errorf("malformed verify frame")
			}
			fr.unread(typ, payload)
			return nil, nil
		})
}

// ── signature cache ──

func cacheDir() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/tmp"
	}
	return filepath.Join(home, ".shuttle_cache")
}

func cacheLoad(filePath string, fi os.FileInfo, blockSize int32, algo string) ([]byte, error) {
	cachePath := cachePathFor(filePath, fi, blockSize, algo)
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, nil
	}
	return data, nil
}

func cacheSave(filePath string, fi os.FileInfo, blockSize int32, algo string, data []byte) error {
	cachePath := cachePathFor(filePath, fi, blockSize, algo)
	dir := filepath.Dir(cachePath)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp := cachePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, cachePath)
}

func cachePathFor(filePath string, fi os.FileInfo, blockSize int32, algo string) string {
	h := sha256.Sum256([]byte(filePath))
	key := fmt.Sprintf("%s_%d_%d_%d_%s.sig",
		hex.EncodeToString(h[:8]),
		fi.ModTime().UnixNano(),
		fi.Size(),
		blockSize,
		algo,
	)
	return filepath.Join(cacheDir(), key)
}

func isEOF(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}
