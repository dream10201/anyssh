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
ANYSSH_SECRET='替换成一个足够长的随机密钥' \
ANYSSH_ADMIN_SECRET='替换成另一条足够长的随机密钥' \
./bin/anyssh-server \
  -listen :8080 \
  -public-url http://1.2.3.4:8080 \
  -client-rotate 30m
```

- `-public-url` 是安装后的客户端连接地址，也是通知消息中链接的地址。
- `-client-rotate` 控制链接刷新周期，设置为 `0` 表示永久不自动刷新。
- `ANYSSH_SECRET` 是必填的客户端注册密钥。修改后，携带旧密钥的客户端无法再次连接。

## 容器运行

容器通过环境变量配置，不需要传入命令行参数：

```bash
docker run -d \
  --name anyssh \
  --restart unless-stopped \
  -p 8080:8080 \
  -e ANYSSH_PUBLIC_URL=http://1.2.3.4:8080 \
  -e ANYSSH_CLIENT_ROTATE=30m \
  -e ANYSSH_SECRET='必填的客户端注册密钥' \
  -e ANYSSH_ADMIN_SECRET='后台管理密钥' \
  -e ANYSSH_WECOM_KEY='企业微信群机器人 key' \
  ghcr.io/dream10201/anyssh:latest
```

环境变量：

| 变量 | 是否必填 | 默认值 | 说明 |
| --- | --- | --- | --- |
| `ANYSSH_LISTEN` | 否 | `:8080` | 容器监听地址 |
| `ANYSSH_PUBLIC_URL` | 否 | 按请求推导 | 客户端及浏览器访问的公网地址 |
| `ANYSSH_CLIENT_ROTATE` | 否 | `30m` | 随机链接轮换周期；`0` 表示永久 |
| `ANYSSH_SECRET` | 是 | 无 | 客户端注册密钥；修改后旧密钥客户端无法重连 |
| `ANYSSH_ADMIN_SECRET` | 否 | `ANYSSH_SECRET` | 后台管理请求头密钥；建议设置为独立密钥 |
| `ANYSSH_WECOM_KEY` | 否 | 空 | 企业微信群机器人 Webhook 中的 `key` 参数 |

GitHub Action 会发布 `linux/amd64`、`linux/arm64` 多平台镜像到 GHCR。每个镜像中的服务端都携带上述 13 种 Linux 客户端。
新的容器构建触发时会自动取消之前仍在运行的容器构建，避免多个 Action 并行消耗资源。

## 客户端一键安装

在需要被控制的 Linux 机器上执行：

```bash
curl -fsSL http://1.2.3.4:8080/install | sh
```

客户端没有命令行配置入口。服务端会在下载时把连接地址、共享密钥和轮换周期写入客户端二进制尾部；脚本下载后校验 SHA-256 并立即后台启动，不生成配置文件。构建过程中产生的基础客户端没有 trailer，无法单独运行。

安装命令可以重复执行。若检测到已有客户端，脚本会先停止旧 systemd 服务或 PID 文件对应的后台进程，再替换二进制并重新启动；因此也可用同一条命令完成客户端更新。

安装脚本兼容 POSIX `/bin/sh`，不要求 Bash。没有 curl 时可以使用：

```bash
wget -qO- http://1.2.3.4:8080/install | sh
```

脚本支持 curl、wget 或 BusyBox wget 下载。若存在 sha256sum、BusyBox sha256sum 或 OpenSSL，会自动校验下载内容；三者都不存在时会显示安全警告但继续安装。cp、chmod、mkdir、rm、od、id、sleep、nohup 等命令缺失时会尝试 install、dd、BusyBox 或安全降级。完全没有下载工具，或没有任何可用的文件落地方式时无法完成安装。

需要开机自启时建议使用 root 安装：

```bash
curl -fsSL http://1.2.3.4:8080/install | sudo bash
```

- root 环境优先安装为 `anyssh-client.service` systemd 服务；没有 systemd 或 systemd 启动失败时自动使用后台模式。
- 普通用户安装到 `~/.local/share/anyssh` 并在后台启动，不会自动开机启动。
- systemd 日志：`journalctl -u anyssh-client`。
- 普通用户日志：`~/.local/share/anyssh/client.log`。

## 管理后台与企业微信

打开 `http://服务端地址/admin/` 可以查看当前连接客户端、设备信息及各自链接，并执行禁用、设置访问有效期和立即更换链接。每台客户端可以单独设置链接轮换分钟数，`0` 表示不自动轮换；设置只会下发给对应客户端。客户端重连时会回传自己的轮换值和版本，服务端默认值 `ANYSSH_CLIENT_ROTATE` 只用于新安装客户端。每个客户端都会生成独立链接。

轮换设置只保存在对应客户端进程内存中。客户端进程重启后会恢复其安装时的 `ANYSSH_CLIENT_ROTATE`；该设计不需要任何落地状态文件。

设置 `ANYSSH_WECOM_KEY` 后，新链接会通过企业微信群机器人通知，内容包含主机名、系统用户、系统架构、匿名设备 ID 和访问链接。服务端使用固定官方地址 `https://qyapi.weixin.qq.com/cgi-bin/webhook/send?key=KEY`。

管理后台的 `/admin`、`/admin/` 和 `/api/admin/` 路由要求请求头 `X-AnySSH-Admin-Secret` 与服务端的 `ANYSSH_ADMIN_SECRET` 一致。未单独设置后台密钥时会复用 `ANYSSH_SECRET`。浏览器不需要保存密钥，可以由 NAS 上的 Caddy 在反向代理时自动注入：

```caddyfile
admin.example.com {
    reverse_proxy https://ssh.example.com {
        header_up Host ssh.example.com
        header_up X-AnySSH-Admin-Secret {$ANYSSH_ADMIN_SECRET}
    }
}
```

启动 Caddy 时，为它设置与 AnySSH 服务端相同的 `ANYSSH_ADMIN_SECRET` 环境变量。`header_up` 会覆盖客户端传入的同名请求头。此配置会让所有能访问 `admin.example.com` 的用户自动通过后台校验，因此该域名应只在 NAS 内网、VPN 或 Caddy 自身的访问控制后开放；AnySSH 源站的 8080 端口也不应直接暴露。

## HTTPS 部署

推荐使用 Caddy 或 Nginx 终止 TLS，并确保反向代理支持 WebSocket。服务端应明确设置公网 HTTPS 地址：

```bash
ANYSSH_SECRET='替换成一个足够长的随机密钥' ./bin/anyssh-server \
  -listen :8080 \
  -public-url https://ssh.example.com \
  -client-rotate 30m
```

客户端安装命令相应变为：

```bash
curl -fsSL https://ssh.example.com/install | sh
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
