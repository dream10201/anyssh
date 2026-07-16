# AnySSH

AnySSH 通过客户端主动建立的 WebSocket 连接，把客户端机器上的 PTY shell 转发到公网服务端。浏览器打开随机链接即可进入终端，不需要再次输入用户名或密码。链接按指定周期自动更换，旧链接及其已有会话会立即失效。

> 随机链接本身就是登录凭证。任何拿到链接的人都能以客户端进程所属的系统用户执行命令。公网部署必须使用 HTTPS，并建议用低权限专用用户运行客户端。

## 构建

要求 Go 1.24 或更高版本。执行一键构建脚本：

```bash
./build.sh
```

脚本会先构建内部 Linux 客户端，再将客户端完整嵌入服务端。最终只生成一个可部署文件：

```text
bin/anyssh-server
```

服务端内嵌当前 Go 工具链支持的全部 Linux 架构客户端：`386`、`amd64`、`arm`、`arm64`、`loong64`、`mips`、`mipsle`、`mips64`、`mips64le`、`ppc64`、`ppc64le`、`riscv64`、`s390x`。其中 32 位 ARM 使用 `GOARM=5`，386 和 MIPS 使用软件浮点基线，以扩大旧设备兼容范围。

安装脚本会依次尝试 `uname`、`arch`、BusyBox、dpkg、rpm、apk、getconf、`/proc/cpuinfo`；如果这些方式都不可用，最后直接读取 `/bin/sh` 或 `/proc/self/exe` 的 ELF 头判断架构、位数和字节序。

修改客户端代码后必须重新执行 `./build.sh`，否则服务端携带的仍是旧客户端。

## 服务端运行

在具有公网 IP 的服务器上启动：

```bash
./bin/anyssh-server \
  -listen :8080 \
  -public-url http://1.2.3.4:8080 \
  -client-rotate 30m \
  -notify-url https://notify.example.com \
  -notify-user your-user \
  -secret '替换成一个足够长的随机密钥'
```

- `-public-url` 是安装后的客户端连接地址，也是通知消息中链接的地址。
- `-client-rotate` 控制通过该服务端安装的客户端多久更换一次链接。
- `-notify-url` 和 `-notify-user` 是必填项，缺少任意一项时服务端拒绝启动。
- `-secret` 会由服务端写入下载客户端的二进制尾部，安装用户无需输入。

## 容器运行

容器通过环境变量配置，不需要传入命令行参数：

```bash
docker run -d \
  --name anyssh \
  --restart unless-stopped \
  -p 8080:8080 \
  -e ANYSSH_PUBLIC_URL=http://1.2.3.4:8080 \
  -e ANYSSH_CLIENT_ROTATE=30m \
  -e ANYSSH_NOTIFY_URL=https://notify.example.com \
  -e ANYSSH_NOTIFY_USER=your-user \
  -e ANYSSH_SECRET='替换成一个足够长的随机密钥' \
  ghcr.io/dream10201/anyssh:latest
```

环境变量：

| 变量 | 是否必填 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `ANYSSH_NOTIFY_URL` | 是 | 无 | 通知 API 的 HTTP(S) 地址 |
| `ANYSSH_NOTIFY_USER` | 是 | 无 | 通知 API 用户 |
| `ANYSSH_LISTEN` | 否 | `:8080` | 容器监听地址 |
| `ANYSSH_PUBLIC_URL` | 否 | 按请求推导 | 客户端及浏览器访问的公网地址 |
| `ANYSSH_CLIENT_ROTATE` | 否 | `1h` | 随机链接轮换周期 |
| `ANYSSH_SECRET` | 否 | 空 | 客户端注册共享密钥 |

GitHub Action 会发布 `linux/amd64`、`linux/arm64` 多平台镜像到 GHCR。每个镜像中的服务端都携带上述 13 种 Linux 客户端。

## 客户端一键安装

在需要被控制的 Linux 机器上执行：

```bash
curl -fsSL http://1.2.3.4:8080/install | bash
```

客户端没有命令行配置入口。服务端会在下载时把连接地址、共享密钥、轮换周期、通知地址和通知用户写入客户端二进制尾部；脚本下载后校验 SHA-256 并立即后台启动，不生成配置文件。构建过程中产生的基础客户端没有 trailer，无法单独运行。

需要开机自启时建议使用 root 安装：

```bash
curl -fsSL http://1.2.3.4:8080/install | sudo bash
```

- root 环境优先安装为 `anyssh-client.service` systemd 服务；没有 systemd 时自动使用 `nohup`。
- 普通用户安装到 `~/.local/share/anyssh` 并通过 `nohup` 启动，不会自动开机启动。
- systemd 日志：`journalctl -u anyssh-client`。
- 普通用户日志：`~/.local/share/anyssh/client.log`。

客户端连接成功后会按服务端配置发送以下 JSON，失败时自动退避重试：

```json
{"user":"your-user","msg":"http://1.2.3.4:8080/s/<随机字符串>/"}
```

通知地址和用户没有默认值，必须在服务端提供。随后下载的客户端会自动携带这些值。

## HTTPS 部署

推荐使用 Caddy 或 Nginx 终止 TLS，并确保反向代理支持 WebSocket。服务端应明确设置公网 HTTPS 地址：

```bash
./bin/anyssh-server \
  -listen :8080 \
  -public-url https://ssh.example.com \
  -client-rotate 30m \
  -notify-url https://notify.example.com \
  -notify-user your-user \
  -secret '替换成一个足够长的随机密钥'
```

客户端安装命令相应变为：

```bash
curl -fsSL https://ssh.example.com/install | bash
```

健康检查地址是 `/healthz`。

## 登录环境与补全

Web 终端使用客户端系统用户配置的 shell，并以交互式登录模式启动。客户端会补齐 `HOME`、`USER`、`LOGNAME`、`SHELL`、`PATH`、`TERM=xterm-256color` 和 `COLORTERM=truecolor`。Bash、Zsh、Fish 会加载各自的登录初始化文件。

Tab 补全随 shell 初始化一起启用。目标机器仍需安装相应补全包，例如 Debian/Ubuntu 的 `bash-completion`；AnySSH 不会修改用户已有的 shell 配置。

## 安全说明

- 使用 HTTPS，否则随机令牌、键盘输入、安装配置和终端输出都可能被网络中间人读取。
- 客户端应以专用低权限用户运行，不要用 root。通过 `sudo bash` 安装时，脚本会优先让服务以原 sudo 用户身份运行。
- 通知渠道必须可信；泄露通知内容等同于泄露终端权限。
- `/install` 下载的客户端二进制包含注册配置，不应暴露给不允许接入该服务端的人。尾部参数带完整性校验但没有加密。
- 轮换无法撤销对方在轮换前已经执行的操作。
- 服务端不存储密码或终端数据；shell 进程运行在客户端机器上。
