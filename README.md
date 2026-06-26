[English](README_EN.md) | 简体中文

# 🚀 Shuttle — Windows 原生 rsync 替代品

[![Go](https://img.shields.io/badge/Go-1.26-blue)](https://go.dev)
[![Platform](https://img.shields.io/badge/Windows-native-purple)]()
[![Version](https://img.shields.io/badge/version-0.1.4.3-green)]()

> 配置文件驱动 · 增量传输 · TUI 面板 · SFTP · 中英双语

**Shuttle** 是一个 Windows 原生的增量文件同步工具。基于 [go-rsync](https://github.com/henryborner/go-rsync) 库（独立 rsync delta 算法 + AVX2/AVX-512 SIMD 加速），通过 `syncd.yaml` 定义多组本地→远程映射，一键推送。

```powershell
shuttle                    # 双击启动 TUI
shuttle push web           # 一键同步
```

## ✨ 特性

- **📋 配置文件驱动** — `syncd.yaml` 定义多组映射
- **🔄 增量传输** — rsync 算法，相同文件零传输
- **🛡 服务器保护** — 按服务器配置保护模式，远端文件永不覆盖/删除
- **🖥 TUI 界面** — 仪表盘、映射管理、服务器管理、文件浏览器、设置
- **🌐 SFTP/SSH** — 本地 → 远程，自动检测密钥
- **💾 大文件友好** — mmap 内存映射，1GB 秒级比对
- **🌍 中英双语** — 设置页一键切换
- **📦 单文件** — 一个 `shuttle.exe` 零依赖

## 📦 安装

从 [Releases](https://github.com/henryborner/shuttle/releases) 下载：

- **`shuttle.exe`** — Windows 主程序
- **`shuttle_linux`** — Linux 远程 agent（TUI 一键部署到服务器）

## 🚀 快速开始

```powershell
.\shuttle.exe                   # 双击进 TUI
.\shuttle.exe tui               # 命令行启动 TUI
.\shuttle.exe list              # 列出任务/服务器
.\shuttle.exe test myserver     # 测试 SSH 连接
.\shuttle.exe push web          # 一键同步
.\shuttle.exe push --dry-run    # 模拟运行，预览变更
```

> 无需手写配置：双击 `shuttle.exe` 进入 TUI 即可。

## 📁 配置文件

```yaml
# syncd.yaml
version: "1.0"
servers:
  - name: myserver
    host: 192.168.1.100
    port: 22
    user: deploy
    key_file: ~/.ssh/id_ed25519

tasks:
  - name: web
    source: E:\projects\web\dist\
    target: myserver:/var/www/html/
    options:
      delete: true
      exclude: ["*.tmp", ".git/"]
```

## ⌨️ CLI

| 命令 | 说明 |
|------|------|
| `shuttle` | 双击启动 TUI |
| `shuttle push [name]` | 执行同步 |
| `shuttle list` | 列出所有任务和服务器 |
| `shuttle config` | 完整配置摘要 |
| `shuttle test <server>` | 测试 SSH 连接 |
| `shuttle init` | 生成配置模板 |

### push 常用参数

| 参数 | 说明 |
|------|------|
| `--dry-run` | 模拟运行，不实际修改文件 |
| `-v` | 详细输出 |
| `-w N` | 并行 worker 数（默认 4） |
| `--algo md5\|xxh64\|sha256` | 校验和算法 |

## 🎮 快捷键

| 按键 | 功能 |
|------|------|
| `Enter` | 同步选中 |
| `A` `E` `D` | 添加/编辑/删除映射 |
| `R` | 直接同步当前映射 |
| `Ctrl+T` | 测试服务器连接 |
| `P` | 编辑保护列表 |
| `Tab` | 切换文件浏览器 |

## 📄 许可证

MIT
