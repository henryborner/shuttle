
// 允许用户注册自定义哈希算法，替换硬编码的 switch
package delta

import (
	"fmt"
	"hash"
	"sync"
)

// ChecksumAlgo 描述一个校验和算法
type ChecksumAlgo struct {
	Name   string           // 算法名称，如 "md5", "sha256", "xxh3"
	New    func() hash.Hash // 哈希构造函
	Length int              // 输出字节
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
}

// Register 注册一个校验和算法（线程安全）
func Register(algo ChecksumAlgo) {
	registryMu.Lock()
	defer registryMu.Unlock()
	registry[algo.Name] = algo
}

// GetAlgo 获取已注册的算法
func GetAlgo(name string) (ChecksumAlgo, error) {
	registryMu.RLock()
	defer registryMu.RUnlock()
	algo, ok := registry[name]
	if !ok {
		return ChecksumAlgo{}, fmt.Errorf("未知的校验和算法: %s (已注 %v)", name, ListAlgos())
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

// SetDefault 设置默认算法
func SetDefault(name string) error {
	if _, err := GetAlgo(name); err != nil {
		return err
	}
	registryMu.Lock()
	defaultAlgo = name
	registryMu.Unlock()
	return nil
}

// GetDefault 获取默认算法
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
