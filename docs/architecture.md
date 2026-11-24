# 系统架构

Moori 是 Docker 与 Docker Compose 的管理界面。每台节点通过本机 Docker Engine API 盘点和维护 Docker 资源，通过官方 Compose CLI 应用项目配置。两条路径均使用节点本机的 `/var/run/docker.sock`。跨主机管理走 Agent 主动连接，远程节点操作自己的 Docker。主服务保存节点、授权、配置和操作记录，负责部署调度。

Moori 采用单实例主服务，主服务内置本机 Docker 执行器；管理其他机器时才需要远程 Agent。本机执行与远程 Agent 均使用独立的 SQLite 执行账本，不共享数据库文件，不依赖外部数据库或消息中间件。

## 进程与边界

| 部分 | 职责 | 源码 |
| --- | --- | --- |
| `moori` | HTTP API、控制台、身份权限、发布、调度、恢复与本机执行 | `cmd/moori`、`internal/control` |
| `moori-agent` | 主动连接、持久化受理、Compose 执行、结果与诊断上报 | `cmd/moori-agent`、`internal/node` |
| 领域模型 | 部署状态、发布编译、不可变快照和摘要 | `internal/core` |
| 连接与入口 | AgentWire 消息、WebSocket 连接及可信网关传输判定 | `internal/transport/agentwire`、`internal/transport/httpproxy` |
| Docker 执行 | 资源采集、Docker Engine 操作和 Compose 调用 | `internal/node/localinv`、`internal/node/nodeops`、`internal/node/executor` |
| 通用基础设施 | SQLite、受限日志存储与周期维护 | `internal/platform` |
| CI 与 Webhook | 接收 Compose 发布及部署请求、独立容器版本更新，以及可选的 Gitea Release 事件 | `internal/control/ci`、`internal/control/integration/gitea` |

主服务提供 HTTP 接口，浏览器和远程 Agent 可分别通过 HTTP 和 WebSocket 直接访问。Agent 主动连接，无需开放节点入站管理端口。浏览器会话、CI token 和 Agent 凭证分别认证，浏览器写请求采用 Go 标准库的跨站保护。经现有 TLS 网关时，主服务通过浏览器 HTTPS 来源或可信网关的转发协议选择 Secure Cookie。私有 Registry 凭证的远程发放使用可信传输检查。配置示例见[控制台网关](deployment.md#控制台网关)。

本机执行器和远程 Agent 共用资源采集与 Docker 执行代码。本机通过进程内通道使用保留身份 `@local`，远程 Agent 通过凭证认证的连接上报。开始执行服务时采集，建立或恢复连接后同步，并在运行期间定期刷新。控制台展示各节点当前代次的连接状态与最后一份资源快照。节点页轮询会分别更新连接状态和快照，并标出过期或部分采集失败的数据。

节点有三类名称信息。节点 ID 和凭证代次用于连接、授权和调度。显示名是平台管理员可编辑的主服务数据，本机的默认显示名为「本机 Docker」，其他节点默认使用 Agent ID。系统主机名来自节点采集，在节点概览单独展示。显示名变更写入平台审计。

## 数据归属

| 数据 | 存储位置 | 作用 |
| --- | --- | --- |
| 项目、目标、发布、部署、共享 Registry、模板、权限及操作记录 | 主服务 SQLite，默认 `moori.db` | 控制台配置、任务和审计 |
| CI 令牌哈希、目标授权、独立容器目标及请求记录 | 主服务 SQLite，默认 `moori.db` | 一枚令牌可授权多个项目和目标，按目标区分权限和幂等请求 |
| 独立容器 Webhook 绑定、凭证哈希和投递键 | 主服务 SQLite，默认 `moori.db` | 绑定到节点代次、容器名和仓库，关联投递与节点操作 |
| 账号、会话哈希、密码失败计数和锁定期限、节点显示名 | 主服务 SQLite，默认 `moori.db` | 会话管理及共享节点展示信息，随主库保存 |
| 节点联系状态和最新资源快照 | 默认 `moori.db.resources` | Docker 管理读取的操作状态；不依赖监控库，重建后由节点重新上报 |
| 节点采样历史和告警 | 默认 `moori.db.metrics` | 与部署和节点管理隔离，可单独配置 |
| 部署输出 | 主服务默认 `moori.db.logs` | 有容量上限的诊断尾部 |
| 本机执行账本及输出暂存 | `moori.db.local.db`、`moori.db.local.db.logs` | 本机执行的持久化受理、防重复、结果重报与恢复 |
| 受理记录、执行阶段、待确认事件、盘点证据 | Agent SQLite，默认 `agent.db` | 防重复启动、断线重报与恢复核查 |
| Agent 输出暂存 | 默认 `agent.db.logs` | 结果上报后的诊断重试 |
| 挂载密钥文件 | 默认 `agent.db.secrets` | 保存仍可能被容器挂载的版本文件 |

数据库应放在服务私有的本地持久化目录。主服务和 Agent 容器的默认数据目录均为 `/data`，分别使用各自的数据卷。镜像通过 `MOORI_DATA_DIR` 固定默认数据库所在目录。Agent 对账本和凭证文件加进程锁；每份账本只由一个节点进程使用。容器中的密钥目录与宿主路径必须相同，因为实际挂载由宿主 Docker 完成。

## 部署链路

1. CI 构建并发布镜像，将固定镜像摘要的 Compose 或模板镜像替换信息交给主服务。
2. 主服务编译发布内容，接收带幂等键的请求，在事务中固定 Compose 内容、目标配置、密钥版本和快照摘要。含外部资源绑定的模板，以及节点内发起的受管 Compose 维护，还将所选节点的身份与凭证代次绑定到快照。普通发布在派发时读取目标的当前节点绑定。
3. 通过权限校验的部署请求绑定执行快照并进入队列。
4. 调度在事务中重新校验权限并占用目标执行槽，向本机执行器或绑定的远程 Agent 派发。
5. 执行器校验快照并持久化受理，先在目标节点拉取镜像，再调用 Docker Compose。
6. 执行事件持久化后上报，主服务事务性处理并确认；重复事件可重放确认。诊断输出独立传输。
7. 结果不确定时保留目标锁，进入核查。断线、超时或取消请求本身都不证明 Docker 已停止。

内存通道只用于唤醒和网络传输。SQLite 保存任务、执行证据和审计；不能用内存队列代替受理或结果提交。

## 节点资源维护

| 操作 | 使用的模型 | 执行路径 |
| --- | --- | --- |
| 容器状态与日志快照、镜像、卷、网络 | 节点代次、资源标识、节点操作记录 | 主服务派发，节点使用 Docker Engine API |
| 容器实时日志 | 节点代次、完整容器 ID、临时中继票据 | 本机直接跟随 Docker 日志，远程 WSS Agent 通过独立流中继 |
| 独立容器配置更新或重部署 | 原容器完整 ID、配置变更、替换容器 ID | 节点读取当前容器配置并创建替换容器，按结果切换或尝试恢复 |
| 独立容器 CI 或 Webhook 更新 | 节点代次、稳定容器名、镜像仓库和摘要 | 同一节点操作队列派发，节点校验容器和仓库后复用替换流程 |
| 外部 Compose 项目维护 | 节点代次、现有 Compose 项目名、规范化配置 | 节点核验 Docker 上已存在该项目后，在原项目名下调用 Compose |
| Moori 受管 Compose 项目维护 | 最近成功部署、目标、节点代次和新执行快照 | 生成新部署申请，复用发布、幂等和执行状态 |

平台管理员可使用节点资源工作区。主服务记录节点变更操作，并在派发时和同节点的部署执行互斥。节点操作结果和浏览器资源快照分别更新。快照负责查看资源，变更仍由节点对照 Docker 实际状态处理。操作异常时按结果状态核对 Docker 现场。详见[部署模型与设计](design.md#节点维护模型)。

## 接入解耦

构建、推送镜像和定义流水线由 Gitea Actions 或其他 CI 负责。受管 Compose 使用发布和部署 API，也可通过配置文件启用 Gitea Release 适配器。正式发布事件匹配 Moori 已保存的 Compose 版本，沿用目标的部署权限和幂等规则。

外部 CI 可使用一枚 `MOORI_TOKEN` 向多个已授权目标提交版本；目标决定派发到本机还是远程 Agent。独立容器默认可走容器部署 API，也保留独立的 Webhook 入口。主服务在同一事务中核对权限、入队和登记幂等键。节点执行更新，CI 通过操作 ID 查询结果。周期拉取 Git 与持续对账由外部系统负责。[版本更新接口](integrations.md#gitea-actions-直接部署独立容器)

## 界面与交付

默认 Makefile 和两个 Dockerfile 构建 `cmd/moori`、`cmd/moori-agent`。当前控制台来自 `internal/control/web` 的 Go 内嵌资源。

控制台源码位于 `internal/control/web`，随主服务编译；页面为无构建步骤的 ES 模块（`app.js` 为入口，`api.js` 提供请求与提示等无状态服务，`ui-components.js` 提供视图构件），第三方库以单文件形式随 `vendor/` 目录内嵌（xterm.js、uPlot），URL 采用 History API 路径路由，服务端对非 API、非资源的 GET 路径回退到控制台页面以支持深链接。Docker 访问和执行代码位于 `internal/node`，供本机执行器和远程 Agent 共用。

控制台行为冒烟测试位于 `internal/control/web/testdata/frontend-smoke`：用 esbuild 打包模块图后在 jsdom 中加载真实页面，覆盖启动、导航、深链接与后退。安装其 npm 依赖（`npm install`）后 `go test` 会自动运行；未安装时该测试跳过，Go 测试保持离线自洽。

## 当前边界

- 单主服务实例，不提供多主写入或跨主机部署事务。
- Compose 采用明确的受管子集，详见 [支持范围](compose-support.md)。
- 回滚恢复部署快照，不恢复卷或外部业务数据。
- 不确定执行由运维核对 Docker 现场并按流程释放锁。数据库恢复按目标放行，运行时限制见 [备份恢复](backup-recovery.md#逐目标恢复服务)。
- 节点监控在独立 SQLite 中保留有限的主机和容器采样，控制台可对照部署事件，并可按配置发送告警与恢复通知。

操作流程见 [部署指南](deployment.md)、[运维](deployment-maintenance.md) 和 [备份恢复](backup-recovery.md)。
