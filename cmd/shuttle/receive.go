// receive.go — 远程 receiver 命令
// 运行在服务器端：读取本地文件 → 生成签名 → 接收指令 → 重建文件
// 通过 stdin/stdout 与发送端通信
package main

import (
	"fmt"
	"os"

	"github.com/henryborner/shuttle/internal/delta"
	"github.com/henryborner/shuttle/internal/util"
	"github.com/spf13/cobra"
)

func init() {
	receiveCmd := &cobra.Command{
		Use:    "receive <文件路径>",
		Short:  "接收端模式（内部使用，由远程 SSH 调用）",
		Hidden: true,
		Run:    runReceive,
		Args:   cobra.ExactArgs(1),
	}
	rootCmd.AddCommand(receiveCmd)
}

func runReceive(cmd *cobra.Command, args []string) {
	filePath := args[0]

	// 1. 打开本地旧文件（流式读签名，不全量入内存）
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

	// 2. 流式生成块签名（不加载全文件）
	blockSize := delta.CalculateBlockSize(fileSize)
	algo := delta.GetDefault()
	sig := delta.GenerateSignatureReader(f, fileSize, blockSize, algo)

	// 3. 发送签名到 stdout
	if err := delta.WireEncodeSignature(os.Stdout, sig); err != nil {
		fmt.Fprintf(os.Stderr, "RECEIVER ERROR: 发送签名失败: %v\n", err)
		os.Exit(1)
	}

	// 4. 从 stdin 读取指令
	// 如果发送端关闭 stdin（表示文件完全匹配，无需重建），正常退出
	instructions, err := delta.WireDecodeInstructions(os.Stdin)
	if err != nil {
		os.Exit(0) // sender aborted: content unchanged, nothing to do
	}

	// 5. 读取 basis 文件用于重建
	// 大文件用 mmap 避免全量读入内存，失败时回退 ReadFile
	oldData, closer, err := util.MmapReadOnly(filePath)
	if err != nil {
		// mmap 失败 → 回退 ReadFile（非 mmap 系统或权限不足）
		oldData, err = os.ReadFile(filePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "RECEIVER ERROR: 重新读取文件失败: %v\n", err)
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
	result, err := recon.Reconstruct(instructions)
	if err != nil {
		fmt.Fprintf(os.Stderr, "RECEIVER ERROR: 重建失败: %v\n", err)
		os.Exit(1)
	}

	// 6. 写入临时文件，然后原子替换
	tmpPath := filePath + ".shuttle_tmp"
	if err := os.WriteFile(tmpPath, result, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "RECEIVER ERROR: 写入临时文件失败: %v\n", err)
		os.Exit(1)
	}

	if err := os.Rename(tmpPath, filePath); err != nil {
		fmt.Fprintf(os.Stderr, "RECEIVER ERROR: 替换文件失败: %v\n", err)
		os.Exit(1)
	}
}
