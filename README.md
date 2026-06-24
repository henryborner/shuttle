[English](README_EN.md) | 简体中文

# 🚀 Shuttle — Windows 原生 rsync 替代品

[![Go](https://img.shields.io/badge/Go-1.26-blue)](https://go.dev)
[![Platform](https://img.shields.io/badge/Windows-native-purple)]()
[![Version](https://img.shields.io/badge/version-0.1.3.3-green)]()

> 配置文件驱动 · 增量传输 · rsync AVX2 校验和 · TUI 面板 · SFTP · 保护列表 · 中英双语

**Shuttle** 是一个 Windows 原生的增量文件同步工具。基于 rsync 算法，移植 rsync AVX2 SIMD 校验和引擎，通过 `syncd.yaml` 定义多组本地→远程映射，一键推送。

```powershell
shuttle push web          # 一键推送
shuttle tui               # 交互式终端面板
```

## ✨ 特性

- **📋 配置文件驱动** — `syncd.yaml` 定义多组映射，一键同步
- **⚡ 三级校验和引擎** — AVX2 (64B/轮, 52 GB/s) / SSE2 (32B/轮, 26 GB/s) / Go 纯标量，自适应调度
- **🔄 增量传输** — rsync 算法滚动校验和 + 哈希块匹配 + 强校验验证，相同文件零传输
- **🔗 算法一致** — \--algo 参数自动同步远端，消除算法不匹配导致的性能退化
- **🛡 服务器保护列表** — 按服务器配置保护模式，远端文件永不覆盖/删除
- **🖥 TUI 界面** — 仪表盘、映射管理、服务器管理、文件浏览器、设置
- **🌐 SFTP/SSH** — 本地 → 远程服务器，支持密钥自动检测
- **💾 大文件优化** — mmap 内存映射，1GB 文件秒级比对
- **🌍 中英双语** — 设置页一键切换
- **📦 单文件** — 一个 `shuttle.exe` 零依赖

## 📦 安装

从 [Releases](https://github.com/henryborner/shuttle/releases) 下载：

- **`shuttle.exe`** — Windows 主程序，在你本地运行
- **`shuttle_linux`** — Linux 远程 agent，部署到服务器（TUI 服务器页 → Enter 自动部署）

或自行构建：

```powershell
git clone https://github.com/henryborner/shuttle.git
cd shuttle
go build -o shuttle.exe ./cmd/shuttle/
```

## 🚀 快速开始

```powershell
.\shuttle.exe init              # 生成配置模板（可选，TUI 也能直接添加）
.\shuttle.exe tui               # 启动 TUI → 映射/服务器页面直接添加
.\shuttle.exe push web          # 一键同步
.\shuttle.exe push --dry-run    # 模拟预览
```

> 无需手动写配置：直接 `shuttle tui` 进入界面，在映射管理和服务器页面用 `A` 添加即可。

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
| `shuttle tui` | 启动 TUI |
| `shuttle push [name]` | 执行同步 |
| `shuttle push --dry-run` | 模拟运行（列出每文件操作 + 高危警告） |
| `shuttle config` | 查看配置文件位置和状态 |
| `shuttle init` | 生成配置文件 |
| `shuttle version` | 版本信息 |

## 🎮 快捷键

| 范围 | 按键 | 功能 |
|------|------|------|
| 仪表盘 | `Enter` | 同步选中 |
| 映射 | `A` `E` `D` | 添加/编辑/删除 |
| 映射 | `R` | 直接同步 |
| 服务器 | `Ctrl+T` | 测试连接 |
| 服务器 | `P` | 保护列表 |
| 保护列表 | `Tab` | 远端文件浏览器 |
| 文件管理 | `Tab` | 浏览本地 |
| 文件管理 | `Ctrl+B` | 浏览远程 |

## 🔧 技术架构

```
cmd/shuttle/          ← Cobra CLI 入口
internal/
├── delta/            ← 增量算法 + AVX2/SSE2 三级校验和引擎
├── transport/        ← SFTP 传输 + SyncEngine + Hook + mmap
├── config/           ← YAML 配置解析
├── i18n/             ← 中英双语
├── util/             ← SSH/mmap 工具
└── tui/              ← Bubble Tea TUI 界面
```

## 📄 许可证

MIT
