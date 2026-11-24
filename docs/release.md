# 发布 moori 与 moori-agent

当前交付两个独立程序，不提供其他名称的兼容别名：

| 用途 | 二进制/镜像名 | 命令目录 | Compose 服务名 | 容器数据目录 |
| --- | --- | --- | --- | --- |
| 主服务 | `moori` | `cmd/moori` | `moori` | `/data` |
| 节点执行 | `moori-agent` | `cmd/moori-agent` | `moori-agent` | `/data` |

主服务默认 HTTP，Web 界面由 Go 内嵌；Agent 使用凭证文件连接。安装和配置见 [部署指南](deployment.md)。

## 二进制

```bash
make build
# bin/moori、bin/moori-agent
sudo make install
# /usr/local/bin/moori、/usr/local/bin/moori-agent

make release-agent VERSION=0.1.1
# dist/release/moori-agent-0.1.1-linux-amd64.tar.gz
# dist/release/moori-agent-0.1.1-linux-arm64.tar.gz
# dist/release/SHA256SUMS、sizes.txt
```

构建需要 Go 1.27.1 或更新的兼容版本，不需要 Node.js/pnpm。Agent 运行环境是 Linux；跨平台构建可设置 `GOOS=linux GOARCH=amd64` 或 `arm64`。

## 镜像

```bash
docker build -f deploy/Dockerfile -t moori:0.1.1 .
docker build -f deploy/Dockerfile.agent -t moori-agent:0.1.1 .
```

默认的主服务镜像和 Agent 镜像都包含 Docker CLI 和 Compose 插件。主服务镜像包含控制台与本机执行器，Agent 镜像用于其他 Linux 节点。两类执行器均通过所在节点的 Docker socket 工作。两个镜像均以 `/data` 持久化各自的数据。

Makefile 使用 `IMG_SERVER=moori` 和 `IMG_AGENT=moori-agent` 默认值。`make release-images` 调用 buildx 构建双架构；要把多架构产物推到镜像仓库，明确执行：

```bash
docker buildx build -f deploy/Dockerfile --platform linux/amd64,linux/arm64 \
  -t REGISTRY/moori:0.1.1 --push .
docker buildx build -f deploy/Dockerfile.agent --platform linux/amd64,linux/arm64 \
  -t REGISTRY/moori-agent:0.1.1 --push .
```

仅管理远程 Agent 时，可用同一个 Dockerfile 的 `control` 目标构建轻量主服务。该目标只打包主程序和运行时基础文件，并默认关闭本机执行。给轻量镜像使用独立的 `-slim` 标签：

```bash
make image-control VERSION=0.1.1
docker buildx build -f deploy/Dockerfile --target control --platform linux/amd64,linux/arm64 \
  -t REGISTRY/moori:0.1.1-slim --push .
```

仓库中的发布命令只推送指定版本镜像。阿里云云效构建任务启用远端缓存时，使用默认缓存标签 `flow-docker-build-cache`。希望 ACR 只接收正式镜像时，在构建任务中关闭远端缓存，或使用不带 `--cache-to` 和 `--cache-from` 的构建命令。[云效镜像缓存配置](https://help.aliyun.com/zh/yunxiao/user-guide/build-and-push-custom-image-repositories)

单架构本地验证可使用 `--platform linux/amd64 --load`。体积以当前版本实际构建结果为准。

## 发布验收

执行 [Linux 验收清单](linux-deployment-acceptance.md)，覆盖主服务启动、网关登录、local 和 Agent 执行、Registry、模板、节点监控和恢复。记录源码提交、产物标签和校验和，以及镜像 digest。Agent 发布包的 `sizes.txt` 包含版本和源码提交。实际 Docker 构建、镜像运行和故障演练不能由 Go 编译检查替代。

保留数据库及必要密钥的备份。恢复使用 `moori prepare-restore` 与目标核查流程，参见 [备份恢复](backup-recovery.md)。

## 手动验证与升级

GitHub Actions 在向 `main` 推送或发起 PR 时运行测试、浏览器回归、静态检查和构建。推送形如 `v0.1.1` 的正式版本标签后，流水线先通过相同检查，再向 GHCR 推送相应版本的主服务和 Agent 双架构镜像，最后在 GitHub Release 提供 Agent 的双架构安装包、SHA256SUMS 和 sizes.txt。发布前必须通过 `make test`、`make vet` 和 `MOORI_BROWSER_BIN=/path/to/chromium make test-browser`；默认 Go 测试跳过浏览器，不能代替这一项。Compose 纳管改动还需在独立的 Linux Docker 环境核对卷和网络绑定、同机端口切换及失败回退。镜像构建后可在该环境执行 `bash scripts/smoke-multinode.sh moori:0.1.1 moori-agent:0.1.1`，核对两个 Docker 引擎的部署结果。脚本需要 Bash、curl、jq，并会启动临时的嵌套 Docker 引擎；它不使用生产环境。

升级运行中的 Moori 时，先备份数据，再从宿主机或外部运维环境更新主服务和 Agent。主服务会在替换时短暂停止，因此不通过这个实例自身的部署队列升级它。具体步骤见[运行要求](compatibility.md#更新)和[备份恢复](backup-recovery.md)。
