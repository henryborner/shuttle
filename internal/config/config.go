// 使用 YAML 格式，定义多组本地→远程映射
package config

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Task struct {
	Name    string  `yaml:"name"`
	Source  string  `yaml:"source"`
	Target  string  `yaml:"target"`
	Options Options `yaml:"options"`
}

// Options 同步选项
type Options struct {
	Delete   bool     `yaml:"delete"`   // 删除目标多余文件
	Exclude  []string `yaml:"exclude"`  // 排除文件模式
	Compress bool     `yaml:"compress"` // SSH 压缩
	Checksum bool     `yaml:"checksum"` // 用校验和判断差异
	Watch    bool     `yaml:"watch"`    // 监听模式（预留）
	Flat     bool     `yaml:"flat"`     // 直接映射内容不套源文件夹名
}

type Server struct {
	Name    string `yaml:"name"`
	Host    string `yaml:"host"`
	Port    int    `yaml:"port"`
	User    string `yaml:"user"`
	KeyFile string `yaml:"key_file"` // SSH 私钥路径
	Pass    string `yaml:"password"` // 或密码（不推荐）
}

// Config 顶层配置
type Config struct {
	Version  string   `yaml:"version"`
	Language string   `yaml:"language,omitempty"` // "en" or "zh"
	Checksum string   `yaml:"checksum,omitempty"` // default checksum algo
	Workers  int      `yaml:"workers,omitempty"`  // delta并行数，0默认=4，1=串行
	Servers  []Server `yaml:"servers"`
	Tasks    []Task   `yaml:"tasks"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %w", err)
	}

	return &cfg, nil
}

func (c *Config) Save(path string) error {
	if c.Version == "" {
		c.Version = "1.0"
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	return os.WriteFile(path, data, 0600)
}

func (c *Config) Validate() error {
	for i, t := range c.Tasks {
		if t.Name == "" {
			return fmt.Errorf("任务 #%d 缺少名称", i+1)
		}
		if t.Source == "" {
			return fmt.Errorf("任务 '%s' 缺少 source", t.Name)
		}
		if t.Target == "" {
			return fmt.Errorf("任务 '%s' 缺少 target", t.Name)
		}
	}
	return nil
}

func (c *Config) GetTask(name string) *Task {
	for i := range c.Tasks {
		if c.Tasks[i].Name == name {
			return &c.Tasks[i]
		}
	}
	return nil
}

// GetServer 按名称查找服务器
func (c *Config) GetServer(name string) *Server {
	for i := range c.Servers {
		if c.Servers[i].Name == name {
			return &c.Servers[i]
		}
	}
	return nil
}

func ParseTarget(target string) (serverName, path string) {
	// IPv6 address in brackets: [::1]:/path or [::1]:path
	if strings.HasPrefix(target, "[") {
		if idx := strings.IndexByte(target, ']'); idx > 0 && idx+1 < len(target) && target[idx+1] == ':' {
			return target[:idx+1], target[idx+2:]
		}
	}
	for i := 0; i < len(target); i++ {
		if target[i] == ':' && i > 0 && target[i-1] != '\\' && i < len(target)-1 && target[i+1] != '\\' {
			return target[:i], target[i+1:]
		}
	}
	return "", target
}
