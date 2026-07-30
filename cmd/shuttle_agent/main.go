// shuttle_agent — minimal remote agent binary for Linux servers.
// Only imports go-rsync + stdlib. No cobra, no TUI, no SSH client, no config.
// shuttle_agent — 远端 Linux 服务器的最小 agent 二进制。
// 仅导入 go-rsync + 标准库。无 cobra、无 TUI、无 SSH 客户端、无配置。
package main

import (
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

const Version = "0.1.5.18"

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

	// 1. Open local old file (stream read signature).
	f, err := os.Open(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "RECEIVER ERROR: 读取文件失败: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		fmt.Fprintf(os.Stderr, "RECEIVER ERROR: stat 失败: %v\n", err)
		os.Exit(1)
	}
	fileSize := fi.Size()

	// 2. Generate or load cached block signatures.
	blockSize := delta.CalculateBlockSize(fileSize)
	var sig *delta.Signature
	var sigWire []byte

	if !*noCache {
		cached, _ := cacheLoad(filePath, fi, blockSize, *algo)
		if cached != nil {
			s, err := delta.WireDecodeSignature(bytes.NewReader(cached))
			if err == nil {
				sig = s
				sigWire = cached
			}
		}
	}
	if sig == nil {
		sig = delta.GenerateSignatureReader(f, fileSize, blockSize, *algo)
		var buf bytes.Buffer
		if err := delta.WireEncodeSignature(&buf, sig); err != nil {
			fmt.Fprintf(os.Stderr, "RECEIVER ERROR: encode signature failed: %v\n", err)
			os.Exit(1)
		}
		sigWire = buf.Bytes()
		if !*noCache {
			if err := cacheSave(filePath, fi, blockSize, *algo, sigWire); err != nil {
				fmt.Fprintf(os.Stderr, "RECEIVER WARNING: signature cache save failed: %v\n", err)
			}
		}
	}

	// 3. Send signature to stdout.
	if _, err := os.Stdout.Write(sigWire); err != nil {
		fmt.Fprintf(os.Stderr, "RECEIVER ERROR: send signature failed: %v\n", err)
		os.Exit(1)
	}

	// 4. Stream-read instructions from stdin → write directly to temp file.
	tmpPath := filePath + ".shuttle_tmp"
	out, err := os.Create(tmpPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "RECEIVER ERROR: 创建临时文件失败: %v\n", err)
		os.Exit(1)
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

	// 5. Read basis file for reconstruction (prefer mmap, fallback ReadFile).
	oldData, closer, err := mmapReadOnly(filePath)
	if err != nil {
		oldData, err = os.ReadFile(filePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "RECEIVER ERROR: 读取文件失败: %v\n", err)
			cleanup()
			os.Exit(1)
		}
	}
	if closer != nil {
		defer closer()
	}

	blockLens := make([]int32, len(sig.BlockSums))
	for i, bs := range sig.BlockSums {
		blockLens[i] = bs.Length
	}
	recon := delta.NewReconstructor(oldData, blockSize, *algo, blockLens)

	// Streaming pipeline: stdin → decode instructions → write output file.
	err = delta.DecodeInstructionsStreamAll(os.Stdin, func(inst delta.MatchResult) error {
		return recon.WriteInstruction(out, inst)
	})
	if err != nil {
		if isEOF(err) {
			cleanup()
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "RECEIVER ERROR: 流式重建失败: %v\n", err)
		cleanup()
		os.Exit(1)
	}

	// 6. Close output file, atomic rename.
	if err := out.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "RECEIVER ERROR: 关闭临时文件失败: %v\n", err)
		cleanup()
		os.Exit(1)
	}
	if err := os.Rename(tmpPath, filePath); err != nil {
		fmt.Fprintf(os.Stderr, "RECEIVER ERROR: 替换文件失败: %v\n", err)
		cleanup()
		os.Exit(1)
	}
	succeeded = true
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
