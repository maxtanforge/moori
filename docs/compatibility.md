# 构建与运行要求

本文件适用于 `moori` 和 `moori-agent`。

| 部分 | 要求 |
| --- | --- |
| Go 构建 | `go.mod` 声明 Go 1.27.1；默认二进制构建不依赖 Node.js |
| 主服务 | 单实例，私有且可持久化的本地 SQLite 数据目录 |
| Agent | Linux，发布脚本提供 amd64 / arm64 产物 |
| Docker | 启用 local 的主服务与远程 Agent 均须能访问各自节点的 `/var/run/docker.sock` |
| Compose | `docker compose` 插件，支持 `up --wait` 和使用到的健康检查字段；不使用独立 v1 命令 |
| 容器镜像 | 主服务与 Agent 的 Dockerfile 均从 `docker:28-cli` 提取 Docker CLI 和 Compose 插件 |
| 数据目录 | Agent 账本、凭证及密钥目录私有；容器密钥目录与宿主绝对路径一致 |
| Git / CI | 节点不需要 Git；外部流水线构建镜像并通过 API 或 webhook 触发部署 |
| 浏览器 | 支持当前内嵌页面所用的标准 JavaScript、Fetch 和 Cookie |

不承诺未经过实机验证的 Docker 版本下限、容量或多版本协议兼容。以目标 Linux 环境运行 [验收清单](linux-deployment-acceptance.md) 确认实际支持情况。

## 更新

主服务和 Agent 使用同一仓库版本构建。更新前停止提交新工作，等待已知执行结束并处置不确定执行，完成数据库与密钥备份。保留持久化数据目录，替换程序后核查连接、目标绑定和健康状态。

使用私有镜像前，在主服务配置[共享仓库凭证](docker-management.md#registry)。本机和 Agent 通过各自的 Docker Engine 拉取镜像。

数据库文件与程序应按备份的版本成套保留。数据库恢复必须按 [备份恢复](backup-recovery.md) 执行；不能将旧快照直接当作运行库启动。
