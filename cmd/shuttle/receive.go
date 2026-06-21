// receive.go — 远程 receiver 命令
// 运行在服务器端：读取本地文件 → 生成签名 → 接收指令 → 重建文件
// 通过 stdin/stdout 与发送端通信
package main

import (
	"fmt"
	"os"

	"github.com/henryborner/shuttle/internal/delta"
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

	// 1. 读取本地旧文件（基础文件）
	oldData, err := os.ReadFile(filePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "RECEIVER ERROR: 读取文件失败: %v\n", err)
		os.Exit(1)
	}

	// 2. 生成块签名
	blockSize := delta.CalculateBlockSize(int64(len(oldData)))
	algo := delta.GetDefault()
	sig := delta.GenerateSignature(oldData, blockSize, algo)

	// 3. 发送签名到 stdout（发送端）
	if err := delta.WireEncodeSignature(os.Stdout, sig); err != nil {
		fmt.Fprintf(os.Stderr, "RECEIVER ERROR: 发送签名失败: %v\n", err)
		os.Exit(1)
	}

	// 4. 从 stdin 读取指令（发送端）
	instructions, err := delta.WireDecodeInstructions(os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "RECEIVER ERROR: 读取指令失败: %v\n", err)
		os.Exit(1)
	}

	// 5. 重建文件（传入各块实际长度，避免尾块复制多余字节）
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

	// 成功
	os.Exit(0)
}
