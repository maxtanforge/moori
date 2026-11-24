# Moori

面向个人和小型团队的 Docker 与 Docker Compose 管理界面。资源和运行状态由 Docker 提供，应用部署由官方 Docker Compose 执行。Moori 提供多节点管理、权限、操作记录和可选的发布流程。主服务和 Agent 分别使用 SQLite，Git/CI 通过 API 或 Gitea webhook 接入。

流水线可按镜像摘要更新独立容器，也可发布和部署受管 Compose 版本。从「系统设置 → 流水线接入」可签发一枚 CI Token，授权多个项目的本机和远程部署目标。Gitea Actions 可以直接调用部署 API。[CI 与 Gitea 接入](docs/integrations.md)

- `moori`：HTTP 服务，提供内嵌控制台、发布管理、调度、审计和本机 Docker 部署。单机使用无需安装 Agent。
- `moori-agent`：部署在其他 Linux 机器上，使用凭证主动连接主服务，执行 Docker 操作、Compose 部署和节点采集。

## 发行版本

当前发布版本为 [v0.1.1](https://github.com/maxtanforge/moori/releases/tag/v0.1.1)。主服务镜像为 `ghcr.io/maxtanforge/moori:0.1.1`，远程节点使用 `ghcr.io/maxtanforge/moori-agent:0.1.1`。两个镜像均支持 Linux AMD64 和 ARM64。

## 构建与启动

构建需要 Go 1.27.1 或更新的兼容版本。以下命令在 Linux 上执行：

```bash
make build
sudo make install
```

默认构建生成 `bin/moori` 和 `bin/moori-agent`，界面从 `internal/control/web` 内嵌，不需要前端构建工具。首次运行 `moori serve` 后，在可信网络内打开 `http://主服务地址:8080`，设置管理员账号和密码。Docker 可用时，「运行环境」会显示自动接入的「本机 Docker」。管理远程机器时，在该页创建 Agent Token，在远端配置 `MOORI_AGENT_TOKEN` 和 `MOORI_ENDPOINT` 后启动 Agent。在「系统设置 → 流水线接入 → 部署记录」创建部署目标，选择本机或远程 Agent。详见 [部署指南](docs/deployment.md)。

主服务镜像使用 `deploy/Dockerfile`，Agent 镜像使用 `deploy/Dockerfile.agent`。主服务容器使用 `/data/moori.db`。Agent 容器使用 `/data/agent.db`。镜像已设置默认数据目录，日常启动省略 `-db`。

默认 `deploy/compose.example.yml` 只启动主服务，挂载宿主 Docker socket 和部分只读系统信息，支持本机部署、资源盘点和监控。主服务内置执行账本为 `/data/moori.db.local.db`，需与主库一起保留。发布版本可通过 CI/API 接入，平台管理员也可在「系统设置」使用 Compose 模板部署。镜像仓库在「系统设置」配置一次，本机和所有 Agent 共用仓库地址与凭证。

## 文档

- [文档索引](docs/README.md)
- [系统架构](docs/architecture.md) · [部署模型与设计](docs/design.md)
- [部署指南](docs/deployment.md) · [运行要求](docs/compatibility.md) · [发布指南](docs/release.md)
- [CI 与 Gitea 接入](docs/integrations.md) · [用户与权限](docs/access.md)
- [Docker 资源管理](docs/docker-management.md) · [Compose 支持范围](docs/compose-support.md) · [密钥管理](docs/secrets.md)
- [部署运维](docs/deployment-maintenance.md) · [备份恢复](docs/backup-recovery.md)
- [节点监控](docs/observability.md) · [Linux 验收](docs/linux-deployment-acceptance.md)

## 验证

在 Linux 上执行 `make test` 和 `make vet`。浏览器交互检查使用：

```bash
MOORI_BROWSER_BIN=/path/to/chromium go test ./internal/control/web -run Browser -v
```

浏览器检查需要 Node.js 和 Chromium，覆盖初始化登录、控制台操作和节点监控。真实 Docker、Agent、网关和 Gitea 联调见 [Linux 验收清单](docs/linux-deployment-acceptance.md)。
