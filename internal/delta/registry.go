// Package delta allows registering custom hash algorithms, replacing hard-coded switches.
// 允许用户注册自定义哈希算法，替换硬编码的 switch。
package delta

import (
	"fmt"
	"hash"
	"sync"
)

// ChecksumAlgo describes a checksum algorithm.
// ChecksumAlgo 描述一个校验和算法。
type ChecksumAlgo struct {
	Name   string           // algorithm name e.g. "md5", "sha256", "xxh3" / 算法名称
	New    func() hash.Hash // hash constructor / 哈希构造函数
	Length int              // output bytes / 输出字节数
}

var (
	registryMu  sync.RWMutex
	registry    = make(map[string]ChecksumAlgo)
	defaultAlgo = "md5"
)

func init() {

	Register(ChecksumAlgo{
		Name:   "md5",
		New:    newMD5,
		Length: 16,
	})
	Register(ChecksumAlgo{
		Name:   "sha256",
		New:    newSHA256,
		Length: 32,
	})
	Register(ChecksumAlgo{
		Name:   "xxh64",
		New:    newXXH64,
		Length: 8,
	})
}

// Register registers a checksum algorithm (thread-safe).
// Register 注册一个校验和算法（线程安全）。
func Register(algo ChecksumAlgo) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[algo.Name] = algo
}

// GetAlgo retrieves a registered algorithm.
// GetAlgo 获取已注册的算法。
func GetAlgo(name string) (ChecksumAlgo, error) {
	registryMu.RLock()
	algo, ok := registry[name]
	registryMu.RUnlock() // release before ListAlgos() to avoid deadlock / 在 ListAlgos() 前释放，避免死锁
	if !ok {
		return ChecksumAlgo{}, fmt.Errorf("unknown checksum algorithm / 未知校验和算法: %s (registered / 已注册: %v)", name, ListAlgos())
	}
	return algo, nil
}

func ListAlgos() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	names := make([]string, 0, len(registry))
	for name := range registry {
		names = append(names, name)
	}
	return names
}

// SetDefault sets the default algorithm.
// SetDefault 设置默认算法。
func SetDefault(name string) error {
	if _, err := GetAlgo(name); err != nil {
		return err
	}
	registryMu.Lock()
	defaultAlgo = name
	registryMu.Unlock()
	return nil
}

// GetDefault returns the current default algorithm name.
// GetDefault 获取默认算法名称。
func GetDefault() string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	return defaultAlgo
}

func MustGet(name string) ChecksumAlgo {
	algo, err := GetAlgo(name)
	if err != nil {
		panic(err)
	}
	return algo
}

func NewHash(algoName string) (hash.Hash, error) {
	algo, err := GetAlgo(algoName)
	if err != nil {
		return nil, err
	}
	return algo.New(), nil
}
