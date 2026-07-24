// Package config uses YAML format to define local→remote mappings.
// 使用 YAML 格式，定义多组本地→远程映射。
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

// Options represents sync options for a task.
// Options 同步选项。
type Options struct {
	Delete   bool     `yaml:"delete"`    // delete extra files on target / 删除目标多余文件
	Exclude  []string `yaml:"exclude"`   // exclude file patterns / 排除文件模式
	Checksum bool     `yaml:"checksum"`  // use checksum to detect changes / 用校验和判断差异
	Flat     bool     `yaml:"flat"`      // map content directly, no source folder wrapping / 直接映射不套源文件夹
	ShowDots bool     `yaml:"show_dots"` // show hidden files/dirs (starting with .) / 显示.开头的隐藏文件
}

type Server struct {
	Name    string   `yaml:"name"`
	Host    string   `yaml:"host"`
	Port    int      `yaml:"port"`
	User    string   `yaml:"user"`
	KeyFile string   `yaml:"key_file"`          // SSH private key path / SSH 私钥路径
	Pass    string   `yaml:"password"`          // or password (not recommended) / 或密码（不推荐）
	Protect []string `yaml:"protect,omitempty"` // protect patterns: remote files never overwritten/deleted / 保护模式：远端文件绝不覆盖/删除
}

// Config is the top-level configuration.
// Config 顶层配置。
type Config struct {
	Version  string   `yaml:"version"`
	Language string   `yaml:"language,omitempty"` // "en" or "zh"
	Checksum string   `yaml:"checksum,omitempty"` // default checksum algo
	Workers  int      `yaml:"workers,omitempty"`  // delta parallel workers; 0=default 4, 1=serial / delta并行数，0默认=4，1=串行
	Servers  []Server `yaml:"servers"`
	Tasks    []Task   `yaml:"tasks"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config failed / 读取配置失败: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config failed / 解析配置失败: %w", err)
	}

	return &cfg, nil
}

func (c *Config) Save(path string) error {
	if c.Version == "" {
		c.Version = "1.0"
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("serialize config failed / 序列化配置失败: %w", err)
	}
	return os.WriteFile(path, data, 0600)
}

func (c *Config) Validate() error {
	for i, t := range c.Tasks {
		if t.Name == "" {
			return fmt.Errorf("task #%d missing name / 任务 #%d 缺少名称", i+1, i+1)
		}
		if t.Source == "" {
			return fmt.Errorf("task '%s' missing source / 任务 '%s' 缺少 source", t.Name, t.Name)
		}
		if t.Target == "" {
			return fmt.Errorf("task '%s' missing target / 任务 '%s' 缺少 target", t.Name, t.Name)
		}
		srvName, _ := ParseTarget(t.Target)
		if srvName != "" && c.GetServer(srvName) == nil {
			return fmt.Errorf("task '%s' references unknown server '%s' / 任务 '%s' 引用了未知服务器 '%s'", t.Name, srvName, t.Name, srvName)
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

// GetServer looks up a server by name.
// GetServer 按名称查找服务器。
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
			// Windows drive letter (e.g. C:/path or D:\) — not a server:path target.
			// Windows 盘符（如 C:/path）— 不是 server:path 格式。
			if i == 1 && ((target[0] >= 'A' && target[0] <= 'Z') || (target[0] >= 'a' && target[0] <= 'z')) {
				continue
			}
			return target[:i], target[i+1:]
		}
	}
	return "", target
}
