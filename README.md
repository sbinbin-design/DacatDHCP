<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="web/dhcp-white.svg">
    <img src="web/dhcp.ico" width="120" alt="DacatDHCP">
  </picture>
</p>

<h1 align="center">DacatDHCP</h1>

> [English](README_EN.md) | 中文

DacatDHCP 是一款 Windows 便携式轻量 DHCP 服务工具。它将 DHCP 服务与图形化管理界面打包为单个 EXE，无需安装、无需额外运行时依赖，双击即可运行，适合自建、测试或隔离网络中快速提供 IP 地址分配服务。

## 核心功能

- **单 EXE 部署**：前端界面、图标、版本资源通过 `go:embed` 编译进二进制，发布只需一个 `DacatDHCP.exe`。
- **图形化管理**：通过浏览器访问 `http://127.0.0.1:8765` 即可管理，支持网卡选择、地址池配置、租约查看、运行日志。
- **网卡绑定**：DHCP 服务只绑定到手动选择的网卡，不影响其他网卡。
- **地址池管理**：自动排除网络地址、广播地址和服务端 IP，最大支持 4096 个地址；支持推荐地址池一键填充。
- **可选网关与 DNS**：网关和 DNS 均为可选项，留空则不下发对应 Option；网关不得位于地址池范围内。
- **租约生命周期**：DISCOVER 生成 60 秒待定 Offer，REQUEST 确认后转为活动租约；REQUEST 仅在请求 IP 可用时 ACK，否则 NAK。
- **深色与浅色主题**：支持浅色 / 深色两种主题，点击顶部主题图标按钮即可切换，选择保存在本地，刷新或重启后保持。
- **中英文国际化**：界面支持中文与英文切换，语言选择保存在本地，首次启动根据系统语言自动选择。
- **系统托盘**：最小化到托盘，双击打开控制台，右键菜单提供状态查看与退出。
- **单实例检测**：重复启动时打开现有控制台并退出当前进程。
- **安全退出**：处理 Windows 关机、注销和会话结束事件，确保释放 DHCP 端口、HTTP 端口和后台协程。

## 支持系统

正式支持：

- Windows 10 / Windows 11
- Windows Server 2016 及以上

需要管理员权限运行（DHCP 服务需要绑定 67 端口）。程序启动时会自动请求提权。

## 运行方式

1. 以管理员身份运行 `DacatDHCP.exe`（程序会自动提权）。
2. 程序启动后自动打开浏览器访问管理控制台 `http://127.0.0.1:8765`。
3. 在控制台选择服务网卡、配置地址池，点击「启动 DHCP」。
4. 最小化后会驻留系统托盘，双击托盘图标可重新打开控制台。

如果 8765 端口被占用，程序会弹出错误并退出，不会留下无界面后台进程。

## 配置说明

配置文件位于程序同目录的 `data/config.json`，首次运行时自动创建。可配置字段：

| 字段 | 说明 |
| --- | --- |
| `adapter_name` | 服务绑定的网卡名称 |
| `pool_start` | 地址池起始 IP |
| `pool_end` | 地址池结束 IP |
| `lease_minutes` | 租约时间（分钟） |
| `web_port` | 管理页面端口（默认 8765） |
| `gateway` | 默认网关（可选，留空则不下发 Option 3） |
| `dns_servers` | DNS 服务器列表（可选，最多 3 个，留空则不下发 Option 6） |

运行日志写入 `data/dhcpsrv.log`。`data` 目录与配置文件不包含在源码和发布包中，运行时自动生成。

## 启动与停止 DHCP

- **启动**：在控制台选择网卡并填写地址池后，点击「启动 DHCP」。运行时配置输入会被锁定。
- **停止**：点击「停止服务」，或通过托盘菜单退出程序。
- 网卡异常（如断开）时服务会自动停止并更新托盘状态。

## 清空日志

- 控制台日志卡片提供「清空日志」按钮，点击后弹出确认对话框。
- 确认后调用 `POST /api/logs/clear`，后端 `Logger.Clear()` 在互斥锁内执行以下流程：
  - 优先在现有文件句柄上执行 `Truncate(0)`，成功后再清空内存环形缓冲区，原句柄保持不变。
  - `Truncate(0)` 失败时（Windows `O_APPEND` 句柄不支持截断）走重新打开回退路径：先用 `O_TRUNC` 打开新句柄，成功后才关闭旧句柄并替换为新句柄，再清空内存环形缓冲区。
  - 重新打开也失败时，保留原文件句柄和内存日志，返回错误，不中断后续日志写入。
- 成功后立即从后端刷新日志列表。
- 禁止只清空前端 DOM 而不通知后端，避免下一次轮询恢复旧日志。

## 主题与语言切换

- **主题**：点击顶部主题图标按钮（浅色下显示月亮、深色下显示太阳）即可在浅色 / 深色之间切换。主题选择保存在浏览器本地存储，刷新或重启后保持，首次默认浅色。
- **语言**：点击顶部「中文｜EN」分段控件切换语言，当前语言会高亮标识。也可在「设置」中选择。首次启动根据系统语言自动选择，默认中文。

## 安全警告

> 本工具仅适用于自建、测试或隔离网络，**禁止在已有 DHCP 服务的生产网络中随意启用**。在同一网络中存在多个 DHCP 服务会导致 IP 分配冲突和网络故障。

DacatDHCP 不会自动启用 Windows 路由、NAT 或网络共享，填写网关仅代表向客户端下发该地址。

## 构建方法

### 环境要求

- Go 1.26.4
- Windows 操作系统（amd64）
- 构建脚本位于 `scripts/` 目录

### 构建步骤

1. 生成 Windows PE 资源（图标与版本信息）：
   ```
   scripts\generate_resource.bat
   ```
2. 构建单 EXE（包含 gofmt、go vet、go test 校验）：
   ```
   scripts\build.bat
   ```
3. 产物输出到 `dist\DacatDHCP.exe`。

构建脚本会校验 Go 版本必须为 1.26.4，并验证 EXE 的版本资源与 `internal/version/versioninfo.json` 一致。

### 测试

- 单元测试：`go test ./...`
- 竞态检测：`scripts\test_race.bat`（需要 C 编译器，CGO_ENABLED=1）

## 单 EXE 发布说明

应用本身为单 EXE，运行只需 `DacatDHCP.exe` 一个文件：

- 前端 HTML/CSS/JS、图标、语言资源、主题脚本全部通过 `go:embed` 编译进二进制。
- 不依赖任何外部 CDN、在线字体或运行时网络依赖。
- 使用 `-ldflags="-H=windowsgui"` 构建为 Windows GUI 子系统，运行无可见 CMD 窗口。
- 版本信息（产品名、版本号、版权）通过 `goversioninfo` 写入 PE 资源，可在文件属性中查看。

正式发布归档（`dist/DacatDHCP-v版本号-windows-amd64.zip`）必须同时包含以下文件：

- `DacatDHCP.exe` — 主程序
- `LICENSE` — Apache License 2.0 许可证原文
- `NOTICE` — 版权与归属声明
- `TRADEMARKS.md` — 商标说明

`scripts/build.bat` 构建完成后会自动生成上述 ZIP 归档。

## 目录结构

```
DacatDHCP/
├── cmd/dacatdhcp/          # 程序入口 main.go
├── internal/
│   ├── dhcp/               # DHCP 核心协议与服务
│   ├── network/            # 网卡枚举与查询
│   ├── server/             # HTTP 管理 API 与配置
│   ├── singleinstance/     # 单实例检测
│   ├── systray/            # 系统托盘
│   └── version/            # 版本信息(唯一源 versioninfo.json)
├── web/                    # 前端资源(通过 go:embed 编译)
│   ├── index.html          # 管理页面
│   ├── style.css           # 样式(含浅色/深色主题与响应式)
│   ├── app.js              # 业务逻辑
│   ├── i18n.js             # 中英文语言资源
│   ├── theme.js            # 主题管理
│   ├── embed.go            # embed 声明
│   └── dhcp.ico            # 图标
├── scripts/                # 构建与测试脚本
└── internal/version/versioninfo.json  # 版本信息唯一源
```

## 常见问题

**Q：为什么需要管理员权限？**
A：DHCP 服务需要绑定 UDP 67 端口（小于 1024 的特权端口），必须以管理员身份运行。

**Q：启动后浏览器没有自动打开？**
A：请手动访问 `http://127.0.0.1:8765`。如果端口被占用，程序会弹出错误并退出。

**Q：地址池推荐为空或提示子网地址空间不足？**
A：所选网卡的子网可用地址过少。请更换网卡或手动指定地址池范围。

**Q：网关为什么不能填在地址池内？**
A：网关若与地址池重叠会导致客户端配置冲突，程序会拒绝启动并提示调整。

**Q：切换语言后日志内容会翻译吗？**
A：不会。DHCP 原始日志保持原样，只翻译界面标签和程序自身生成的提示。

## 许可证

DacatDHCP 基于 Apache License 2.0 开源。

Copyright 2026 DACAT.CC.

再发布时必须保留适用的许可证、版权和归属声明。详见 [LICENSE](LICENSE)、[LICENSE.zh-CN.md](LICENSE.zh-CN.md)、[NOTICE](NOTICE) 和 [TRADEMARKS.md](TRADEMARKS.md)。
