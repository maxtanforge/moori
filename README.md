# Moori

Moori 是面向个人和小型团队的 Docker 管理控制台。在浏览器中切换节点，管理容器、Compose 项目、镜像、网络和卷，查看实时日志、容器终端和资源监控。主服务直接管理本机 Docker；其他 Linux 主机通过 Agent 主动接入。

| 部署位置 | v0.2.0 镜像 | 持久化数据 |
| --- | --- | --- |
| 控制台所在主机 | `ghcr.io/maxtanforge/moori:0.2.0` | `/data/moori.db` 和本机执行账本 `/data/moori.db.local.db` |
| 每台远程 Docker 主机 | `ghcr.io/maxtanforge/moori-agent:0.2.0` | `/data/agent.db` |

两个镜像均支持 Linux AMD64 和 ARM64。升级时保留各自原有的 `/data` 挂载。[v0.2.0 发行说明](https://github.com/maxtanforge/moori/releases/tag/v0.2.0)

## 启动本机控制台

在安装了 Docker 和 Compose 的 Linux 主机上，从仓库根目录运行：

```bash
docker compose -f deploy/compose.example.yml up -d
```

在可信网络打开 `http://主服务地址:8080`，首次访问时创建管理员账号。进入「运行环境 → 本机 Docker」即可查看已有容器。单机部署只需主服务。[部署示例](deploy/compose.example.yml) · [运行要求](docs/compatibility.md)

## 接入和升级远程 Agent

1. 在控制台「运行环境」创建 Agent Token。
2. 在远程主机配置 `MOORI_ENDPOINT` 和 `MOORI_AGENT_TOKEN`，使用 Agent 镜像启动，并挂载该机的 Docker socket 和持久化数据目录。
3. 在「运行环境」确认该节点在线，进入节点即可管理其 Docker 资源。[远程接入步骤](docs/deployment.md#在网页接入远程机器) · [Agent 部署示例](deploy/compose.agent.example.yml)

**从 v0.1.0 升级 Agent 到 v0.2.0，请从远程宿主机执行。** 旧 Agent 在控制台替换自身容器时会先停止自己，可能留下尚未启动的新版容器。[Agent 升级与中断恢复](docs/deployment.md#升级远程-agent) · [删除远程节点](docs/deployment.md#删除远程节点)

## 使用指南

| 要做什么 | 文档 |
| --- | --- |
| 管理容器和共享镜像仓库，维护 Docker 资源 | [Docker 资源管理](docs/docker-management.md) |
| 在节点粘贴 YAML 创建项目，维护已有 Compose 源文件 | [Compose 支持范围](docs/compose-support.md) |
| 从 Gitea Actions 或其他 CI 部署到多个节点 | [CI Token、部署 API 和 Webhook](docs/integrations.md) |
| 查看节点健康、容器指标与告警 | [监控](docs/observability.md) |
| 管理账号与密钥，备份或恢复数据 | [用户权限](docs/access.md) · [密钥管理](docs/secrets.md) · [备份与恢复](docs/backup-recovery.md) |
| 了解架构、部署模型与发布流程 | [系统架构](docs/architecture.md) · [部署模型](docs/design.md) · [发布指南](docs/release.md) |

## 从源码构建

在 Linux 上使用 Go 1.27.1 或更新的兼容版本。Web 控制台已嵌入主服务。

```bash
make build
make test
make vet
```

构建产物为 `bin/moori` 和 `bin/moori-agent`，可用 `sudo make install` 安装。浏览器交互测试需要 Node.js 和 Chromium。

```bash
MOORI_BROWSER_BIN=/path/to/chromium make test-browser
```

[完整文档索引](docs/README.md) · [Linux 环境验收](docs/linux-deployment-acceptance.md)
