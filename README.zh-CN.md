# Polemo Station 中文说明

Polemo Station 是 Polemo 音乐生态的自托管 Station 服务，提供本地音乐库、设备同步、文件传输、元数据与封面处理，以及内置 Web 界面。

[English README](README.md)

## 项目状态

Polemo Station 仍在积极开发中。随着 Polemo 客户端和服务端持续演进，API、配置项和构建流程可能发生变化。

## 功能

- 基于 SQLite 的本地音乐库管理
- 设备配对、授权、同步和文件传输
- 元数据、歌词和封面抓取集成
- 由 Station 二进制直接提供的 Web 界面
- 基于 FFmpeg 的可选转码
- 在支持的网络环境中提供 mDNS 发现

## 环境要求

- Go 1.27 或更高版本
- 需要转码时，确保 FFmpeg 在 `PATH` 中，或通过 `stationFFmpegPath` 指定路径
- 只有重新构建内置 Web 前端时才需要 Node.js 和 pnpm

Go 依赖会由 Go 工具链自动下载。

## 快速开始

### 开发环境

```bash
git clone https://github.com/musqca/polemo-station.git
cd polemo-station
go run . --config config-dev.yaml api
```

默认开发端口为 `20156`。服务会将 SQLite 数据库和音乐库数据存放在 Polemo runtime 管理的应用数据目录中。

正式部署时，请创建自己的配置文件，至少设置以下内容：

```yaml
port: 20156
stationId: "my-station"
stationPairingToken: ""
stationPublicBaseUrl: "http://192.168.1.100:20156"
stationFFmpegPath: ""
stationMediaRoots:
  - "/path/to/your/music"
```

`stationPublicBaseUrl` 必须是 Station 客户端可以访问的 `http://` 或 `https://` 地址。若使用手机或其他设备绑定超管，不要填 `127.0.0.1` 或 `localhost`，应填写局域网 IP 或可访问的域名。

不要在生产环境直接复用示例开发配置中的密钥。

## 绑定第一个超管账号

Station 第一次启动时，如果数据库中还没有有效的超管账号，API 启动流程会在交互式终端输出一次性超管二维码：

```bash
go run . --config config.yaml api
```

使用 Polemo App 扫描终端中的二维码并完成授权，即可绑定第一个超管账号。绑定成功后，该账号会获得 `superadmin` 角色，二维码中的一次性票据也会失效。

注意事项：

- 二维码票据有效期为 2 分钟。
- 只有在交互式终端中运行时，二维码才会直接绘制在终端里；非交互式输出只会提示二维码未渲染。
- 二维码只能在没有有效超管账号时创建。
- 如果重启后没有重新显示二维码，可能是数据库中仍存在未过期的待处理票据；请等待票据过期，或使用下面的重置命令。
- 绑定设备必须能够访问 `stationPublicBaseUrl` 指向的地址。

## 重置超管账号

如果无法再使用原超管账号，或者需要重新绑定第一个超管，可以使用本地恢复命令：

```bash
go run . --config config.yaml reset-superadmin
```

执行前建议先停止正在运行的 Station API。该命令会：

- 撤销所有有效的超管账号
- 撤销这些账号的 App 凭据和 Web 会话
- 撤销待处理或已批准的授权二维码票据
- 将 Station 访问模式恢复为公开模式

命令成功后会提示重启 Station。重启 API：

```bash
go run . --config config.yaml api
```

重启时会在交互式终端输出新的超管二维码，使用 Polemo App 扫描即可完成重新绑定。该操作会使原有超管账号和相关登录会话失效，请确认使用的是正确的数据目录和配置文件。

## 常用命令

查看帮助：

```bash
go run . --help
```

启动 API：

```bash
go run . --config config.yaml api
```

重置超管授权：

```bash
go run . --config config.yaml reset-superadmin
```

`dal` 命令用于开发期间根据数据库表生成 model 和 DAL：

```bash
go run . --config config.yaml dal
```

## 开发

运行测试：

```bash
go test ./...
```

构建本地二进制：

```bash
go build -o dist/polemo-station .
```

Web 前端资源会从 `internal/frontend/build` 嵌入二进制。若要更新内置前端并构建发布版本，请确保相邻目录中存在 [`polemo-station-web`](https://github.com/musqca/polemo-station-web) 项目，然后执行：

```bash
make frontend
make darwin
make linux
make windows
```

也可以一次构建 Makefile 中配置的所有发布目标：

```bash
make pkg
```

## 目录结构

```text
cmd/                 CLI 命令和 API 启动逻辑
internal/config.go   Station 配置默认值和类型
internal/router/     HTTP 和 WebSocket 路由
internal/station/    Station 服务和同步逻辑
internal/frontend/   内置 Web 前端资源
doc/                 设计文档和未来工作记录
```

## 相关项目

- [Polemo Station Web](https://github.com/musqca/polemo-station-web)
- [Polemo Server](https://github.com/musqca/polemo-server)
- [Polemo App](https://github.com/musqca/polemo-app)
