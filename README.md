[English](README_EN.md) | 简体中文

# Shuttle — Windows 原生增量文件同步工具

**Shuttle** 是一个 Windows 原生的文件同步工具，通过 `syncd.yaml` 定义本地→远程映射，一键推送。基于 [go-rsync](https://github.com/henryborner/go-rsync) 库实现 rsync delta 算法，与标准 rsync 线协议不兼容（使用 CHAR_OFFSET=31 的自有线协议）。

```powershell
shuttle                    # 双击启动 TUI
shuttle push web           # 一键同步
```

## 功能

- **配置文件驱动** — `syncd.yaml` 定义多组映射
- **增量传输** — rsync 算法，文件未变化时仅传输校验签名，不传数据块
- **服务器保护** — 按服务器配置保护模式，远端文件不被覆盖或删除
- **TUI 界面** — 仪表盘、映射管理、服务器管理、文件浏览器、设置
- **SFTP/SSH** — 本地 → 远程，自动检测密钥
- **mmap 内存映射** — 大文件比对使用 mmap，减少内存拷贝
- **中英双语** — 设置页切换
- **单文件** — `shuttle.exe`，无额外依赖

## 安装

从 [Releases](https://github.com/henryborner/shuttle/releases) 下载：

- **`shuttle.exe`** — Windows 主程序
- **`shuttle_linux`** — Linux 远程 agent（通过 TUI 部署到服务器）

## 快速开始

```powershell
.\shuttle.exe                   # 双击进 TUI
.\shuttle.exe tui               # 命令行启动 TUI
.\shuttle.exe list              # 列出任务/服务器
.\shuttle.exe test myserver     # 测试 SSH 连接
.\shuttle.exe push web          # 一键同步
.\shuttle.exe push --dry-run    # 模拟运行，预览变更
```

> 双击 `shuttle.exe` 进入 TUI 即可创建配置，无需手写 YAML。

## 配置文件

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

## CLI

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

## 快捷键

| 按键 | 功能 |
|------|------|
| `Enter` | 同步选中 |
| `A` `E` `D` | 添加/编辑/删除映射 |
| `R` | 直接同步当前映射 |
| `Ctrl+T` | 测试服务器连接 |
| `P` | 编辑保护列表 |
| `Tab` | 切换文件浏览器 |

## 工作原理

### 增量传输（rsync delta 算法）

Shuttle 使用 rsync 的 delta 传输算法来减少网络传输量：

1. **分块** — 将源文件按固定大小（默认 2048 字节）切分为数据块
2. **签名** — 对每个块计算两个校验和：一个快速滚动校验和（用于快速匹配）和一个强校验和（xxh64/md5/sha256，用于最终确认）
3. **匹配** — 远端收到签名列表后，在自己的文件副本上滑动窗口搜索匹配块
4. **delta** — 只传输不匹配的字节序列（literal bytes），匹配的块只发送引用
5. **重构** — 远端根据 delta 指令从已有文件拷贝匹配块 + 插入新数据，生成完整文件

如果文件两端完全相同，整个过程只传输签名列表（约几 KB），无需传输文件数据。

### 线协议

Shuttle 使用自有的二进制线协议（非标准 rsync 协议），参数选择：

- **CHAR_OFFSET = 31**：字符偏移参数，影响滚动校验和的碰撞特性
- **默认强校验和 = xxh64**：64 位 xxHash，在速度和碰撞抵抗间取得平衡
- 支持 md5（128 位）、sha256（256 位）作为备选强校验和

### 服务器保护

每个服务器可配置保护列表（glob 模式），匹配的远端文件**不会被覆盖或删除**。适用于保护数据库文件、证书、配置文件等远端关键数据。

### 远端 Agent

Shuttle 通过 SSH 连接到 Linux 服务器，并在远端运行一个轻量 `shuttle_linux` agent。agent 负责：
- 扫描远端文件系统
- 接收签名列表并执行块匹配
- 根据 delta 指令重构文件

可通过 TUI 的服务器页面一键部署或更新 agent。

## 许可证

MIT
