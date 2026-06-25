// receive.go — remote receiver command
// Runs on the server side: read local file → generate signature → receive instructions → rebuild file.
// Communicates with the sender via stdin/stdout.
// receive.go — 远程 receiver 命令
// 运行在服务器端：读取本地文件 → 生成签名 → 接收指令 → 重建文件。
// 通过 stdin/stdout 与发送端通信。
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/henryborner/shuttle/internal/delta"
	"github.com/henryborner/shuttle/internal/util"
	"github.com/spf13/cobra"
)

// cacheDir returns the signature cache directory (~/.shuttle_cache/).
// cacheDir 返回签名缓存目录。
func cacheDir() string {
	home, _ := os.UserHomeDir()
	if home == "" {
		home = "/tmp"
	}
	return filepath.Join(home, ".shuttle_cache")
}

// cacheLoad tries to load a cached signature for the given file.
// Returns the wire-encoded signature bytes, or nil if no valid cache exists.
// cacheLoad 尝试加载缓存签名，无有效缓存时返回 nil。
func cacheLoad(filePath string, fi os.FileInfo, blockSize int32, algo string) ([]byte, error) {
	cachePath := cachePathFor(filePath, fi, blockSize, algo)
	data, err := os.ReadFile(cachePath)
	if err != nil {
		return nil, nil // cache miss is not an error / 缓存未命中不算错误
	}
	return data, nil
}

// cacheSave saves a wire-encoded signature to the cache.
// Uses atomic write (tmp + rename) for safety.
// cacheSave 将签名原子写入缓存。
func cacheSave(filePath string, fi os.FileInfo, blockSize int32, algo string, data []byte) {
	cachePath := cachePathFor(filePath, fi, blockSize, algo)
	dir := filepath.Dir(cachePath)
	os.MkdirAll(dir, 0700)

	tmp := cachePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return // silent fail / 静默失败
	}
	os.Rename(tmp, cachePath)
}

// cachePathFor builds the cache file path from file identity.
// cachePathFor 根据文件身份构建缓存路径。
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

// isEOF checks whether the sender closed stdin early (file is identical / no update needed).
// isEOF 判断是否发送端提前关闭 stdin（文件完全匹配/无需更新）。
func isEOF(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)
}

func init() {
	receiveCmd := &cobra.Command{
		Use:    "receive <file path> / receive <文件路径>",
		Short:  "Receiver mode (internal, called by remote SSH) / 接收端模式（内部使用，由远程 SSH 调用）",
		Hidden: true,
		Run:    runReceive,
		Args:   cobra.ExactArgs(1),
	}
	receiveCmd.Flags().String("algo", "md5", "strong checksum algorithm / 强校验和算法")
	receiveCmd.Flags().Bool("no-cache", false, "skip signature cache (for checksum mode) / 跳过签名缓存（校验和模式用）")
	rootCmd.AddCommand(receiveCmd)
}

func runReceive(cmd *cobra.Command, args []string) {
	filePath := args[0]
	algo, _ := cmd.Flags().GetString("algo")
	noCache, _ := cmd.Flags().GetBool("no-cache")

	// 1. Open local old file (stream read signature, don't load entire file into memory).
	// 1. 打开本地旧文件（流式读签名，不全量入内存）。
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
	//    Hit: skip disk read entirely. Miss: read file + compute + cache.
	// 2. 生成或加载缓存块签名。
	//    命中：完全跳过读盘。未命中：读文件 + 计算 + 写缓存。
	blockSize := delta.CalculateBlockSize(fileSize)
	var sig *delta.Signature
	var sigWire []byte

	var cached []byte
	if !noCache {
		cached, _ = cacheLoad(filePath, fi, blockSize, algo)
	}
	if cached != nil {
		s, err := delta.WireDecodeSignature(bytes.NewReader(cached))
		if err == nil {
			sig = s
			sigWire = cached
		}
	}
	if sig == nil {
		sig = delta.GenerateSignatureReader(f, fileSize, blockSize, algo)
		var buf bytes.Buffer
		if err := delta.WireEncodeSignature(&buf, sig); err != nil {
			fmt.Fprintf(os.Stderr, "RECEIVER ERROR: encode signature failed / 签名编码失败: %v\n", err)
			os.Exit(1)
		}
		sigWire = buf.Bytes()
		if !noCache {
			cacheSave(filePath, fi, blockSize, algo, sigWire)
		}
	}

	// 3. Send signature to stdout.
	// 3. 发送签名到 stdout。
	if _, err := os.Stdout.Write(sigWire); err != nil {
		fmt.Fprintf(os.Stderr, "RECEIVER ERROR: send signature failed / 发送签名失败: %v\n", err)
		os.Exit(1)
	}

	// 4. Stream-read instructions from stdin → write directly to temp file (low memory).
	// 4. 从 stdin 流式读取指令 → 直接写临时文件（低内存）。
	tmpPath := filePath + ".shuttle_tmp"
	out, err := os.Create(tmpPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "RECEIVER ERROR: 创建临时文件失败: %v\n", err)
		os.Exit(1)
	}
	// Ensure temp file is cleaned up on any error path (os.Exit skips defer, so closure).
	// 确保任何错误路径都清理临时文件（os.Exit 不执行 defer，故用闭包封装）。
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
	// mmap doesn't load the entire file into physical memory; OS pages on demand.
	// Ideal for low-memory servers.
	// 5. 读取 basis 文件用于重建（优先 mmap，失败回退 ReadFile）。
	// mmap 不会将整个文件装入物理内存，OS 按需换页，适合小内存服务器。
	oldData, closer, err := util.MmapReadOnly(filePath)
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
	recon := delta.NewReconstructor(oldData, blockSize, algo, blockLens)

	// Streaming pipeline: stdin → decode instructions one by one → write output file.
	// 流式管道：stdin → 逐条解码指令 → 逐条写入输出文件。
	err = delta.DecodeInstructionsStream(os.Stdin, func(inst delta.MatchResult) error {
		return recon.WriteInstruction(out, inst)
	})
	if err != nil {
		// Sender closed stdin = file is perfectly matched → no rebuild needed, clean exit.
		// 发送端关闭 stdin 表示文件完全匹配 → 无需重建，正常退出。
		if isEOF(err) {
			cleanup()
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "RECEIVER ERROR: 流式重建失败: %v\n", err)
		cleanup()
		os.Exit(1)
	}

	// 6. Close output file, atomic rename.
	// 6. 关闭输出文件，原子替换。
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
