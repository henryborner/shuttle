// shuttle_agent — minimal remote agent binary for Linux servers.
// Only imports go-rsync + stdlib. No cobra, no TUI, no SSH client, no config.
// shuttle_agent — 远端 Linux 服务器的最小 agent 二进制。
// 仅导入 go-rsync + 标准库。无 cobra、无 TUI、无 SSH 客户端、无配置。
package main

import (
	"bufio"
	"bytes"
	"crypto/sha256"
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

const Version = "0.1.5.19"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: shuttle receive|identify|version")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "receive":
		runReceive()
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
	fmt.Printf("SHuTtL3_AgEnT_lD:%s:%s/%s:%s\n",
		Version, runtime.GOOS, runtime.GOARCH,
		strings.Join(delta.ListAlgos(), ","))
}

// ── receive command (copied from cmd/shuttle/receive.go, decoupled from cobra) ──

func runReceive() {
	// Parse flags manually: shuttle receive [--algo md5] [--no-cache] <filepath>
	fs := flag.NewFlagSet("receive", flag.ExitOnError)
	algo := fs.String("algo", "md5", "strong checksum algorithm")
	noCache := fs.Bool("no-cache", false, "skip signature cache")
	fs.Parse(os.Args[2:])

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "RECEIVER ERROR: missing file path")
		os.Exit(1)
	}
	filePath := fs.Arg(0)
	if err := receiveFile(filePath, os.Stdin, os.Stdout, *algo, *noCache); err != nil {
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

	// 4. Stream-read instructions from stdin → write directly to temp file.
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

	// 5. Read basis file for reconstruction (prefer mmap, fallback ReadFile).
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

	// The signature-read handle is no longer needed after mmap — close it so
	// the final rename can replace the file on Windows (an open handle there
	// causes "Access is denied"). Linux is unaffected but this is harmless.
	// 签名读取用的句柄在 mmap 后不再需要——关闭它，否则 Windows 上 rename
	// 覆盖仍被打开的句柄会报 "Access is denied"。Linux 不受影响，此举无害。
	if err := f.Close(); err != nil {
		return fmt.Errorf("close signature handle: %w", err)
	}

	blockLens := make([]int32, len(sig.BlockSums))
	for i, bs := range sig.BlockSums {
		blockLens[i] = bs.Length
	}
	recon, err := delta.NewReconstructor(oldData, blockSize, algo, blockLens)
	if err != nil {
		return fmt.Errorf("create reconstructor failed: %w", err)
	}

	// Streaming pipeline: stdin → decode instructions → write output file.
	err = delta.DecodeInstructionsStreamAll(stdin, func(inst delta.MatchResult) error {
		return recon.WriteInstruction(bw, inst)
	})
	if err != nil {
		if isEOF(err) {
			// Sender closed stdin without an EOS marker — treat as "abandon":
			// nothing is written, the temp file is removed by cleanup.
			// 发送端未发 EOS 就关闭——视为放弃本次，临时文件由 cleanup 删除。
			return nil
		}
		return fmt.Errorf("流式重建失败: %w", err)
	}

	// Flush buffered writes before verify/rename.
	// 先 flush 缓冲，再校验/重命名。
	if err := bw.Flush(); err != nil {
		return fmt.Errorf("flush 失败: %w", err)
	}

	// 5.5. Optional verify: sender may send a SHA256 trailer after EOS.
	var expectedHash *[32]byte
	{
		flag := make([]byte, 1)
		if n, _ := io.ReadFull(stdin, flag); n == 1 && flag[0] == 0x01 {
			var h [32]byte
			if _, err := io.ReadFull(stdin, h[:]); err != nil {
				return fmt.Errorf("read verify hash failed: %w", err)
			}
			expectedHash = &h
		}
	}

	// 6. Close output file, atomic rename.
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
