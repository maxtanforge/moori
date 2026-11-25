# 部署运维

由 Agent 执行的节点变更在执行前登记到本地账本。Agent 重启后会补报已经保存的结果；若重启前未确认结果，记录会提示核对 Docker 现场，程序不会自动重执行。补报保留结果和资源标识，详细输出只在原执行会话中上报。

本文适用于 `moori` / `moori-agent`，涵盖执行处置、日志、保留策略与节点维护。主服务与 Agent 的本地命令在各自的[数据目录](deployment.md#数据目录)中运行。

## 失联任务与目标重新分配

过期或断线本身不证明 Docker 已停止。正常情况下保留目标锁，由 Agent 的终态报告释放；有完整账本的中断执行仍可使用 `moori-agent retire-execution` 记录现场核查结果。

仅在原 Agent 无法提供结果时使用以下流程：

1. 停止旧主机上的 Agent、部署子进程和可能继续执行变更的 Docker 操作，或对旧主机实施物理隔离。仅断开控制网络不够；已受理的任务会继续运行。记录检查证据。
2. 使用 `issue-agent-credential -agent AGENT_ID` 为旧 Agent ID 生成更高 generation 的新凭证。这会拒绝旧代次的新连接和现有连接上的后续请求；同一 Agent 下其他目标也会受影响，需逐一核查后重新分配。替换节点的私有凭证文件并重启；控制面只存储凭证哈希。
3. 历史 offer 已过期后执行：

   ```sh
   moori abandon-deployment -id DEPLOYMENT_ID \
     -operator OPERATOR -host-fenced -note "旧主机停止执行的证据与现场检查结果"
   ```

   命令要求物理停机声明和数据库中的更高身份代次同时成立；把不确定结果记为 `abandoned`，清除目标锁和不可信的 last-success 指针，并在同一事务记录操作人和检查说明。相同请求可重试，变更说明的重试被拒绝。恢复数据库实例必须继续使用既有 `restore-release-target` 流程。

4. 将目标分配到已注册的替代 Agent 或新代次，恢复排队工作：

   ```sh
   moori assign-target -project team -target prod \
     -agent host-01 -generation 2
   ```

   分配操作校验项目、当前身份代次、目标空闲状态和恢复状态，并记录审计。原目标仍有活动任务时拒绝修改。

## 密钥文件清理

部署成功或失败不能作为删除依据：容器可能仍挂载 bundle 内的文件。保留自动清理“确认从未启动”的路径，并提供离线维护命令回收不再挂载的 bundle。

停止 Agent，暂停所有外部容器/挂载变更；处理账本中尚未终结的执行。以下命令使用 Agent 数据目录中的默认 `agent.db` 与 `agent.db.secrets`。若运行时设置了自定义路径，清理时使用相同的 `-db` 和 `-secret-dir`。

```sh
# 默认只列出候选；默认最小保留年龄为 24h。
moori-agent gc-secrets -docker-quiescent
# 核对输出后执行删除。
moori-agent gc-secrets -docker-quiescent -apply
```

命令持有 Agent 账本的独占锁，拒绝存在活动账本记录的情况；查询本机 Docker 的全部容器（包括已停止容器）的挂载。任何枚举或解析错误均中止。被文件挂载、目录挂载或父目录挂载覆盖的 bundle 保留。候选目录禁止符号链接和嵌套目录，逐文件删除，不递归遍历任意路径。维护时仍须保证外部容器操作暂停；两次容器清单检查不能代替这一条件。

`-min-age` 可调整最小年龄。运行时预算仍默认 64 MiB / 1024 个 bundle，接近 80% 时记录 Warn；空间释放前不会绕过配额继续写入。

## 保留策略

- 独立容器 CI 和 Webhook 的去重回执在对应凭证或 Webhook 仍有效时继续保留。节点操作页面的 30 天历史窗口和临时输出清理不影响这些回执。

- `moori serve -template-version-retention 20`：选择开启后，启动时及每小时只保留每个控制台 Compose 模板的最新 20 个版本。默认 `0` 保留全部版本；开启前请备份主库，清理后不能从模板重新选用已删除的版本。

- `moori-agent -ledger-retention 720h`：默认启动时及每小时调用 `Ledger.Prune`，仅删除 offer 截止时间超过保留期、已终态且全部事件已获确认的执行。持久化过期下限继续防止删除后时钟回拨导致重放；活动和未确认记录保留。设置 `0` 禁用。恢复旧中心备份时，超出该窗口的本地历史可能缺失，需要现场检查。
- `moori serve -outbox-retention 720h`：默认启动时及每小时按此窗口清理两类历史：过期的终态 outbox（每次最多 5000 行），以及超窗的部署与项目审计记录（`deployment_audit`、`project_audit`，各自每次最多 5000 行）。设置 `0` 可全部保留。节点告警 Webhook 使用独立队列，参见[节点监控](observability.md#通知)。执行 receipts 不由此清理，幂等重放仍可验证。

## 空闲轮询与输出重试

空闲轮询先作只读候选检查，没有候选工作时不申请写事务；有候选时仍在原写事务中重新检查权限、占槽和过期条件。即使候选检查后出现新任务，也仅延迟到下一轮轮询。

输出 ACK 支持可选 `retry` 字段。控制端将授权查询故障、存储不可用和可恢复写入故障标为可重试并记录 Warn；永久拒绝与内容冲突不重试。Agent 保留本地副本并退避 30 秒，正常状态上报不受此 ACK 阻断。输出仍受本地 spool 总额度限制，长时间故障下旧日志可能被容量淘汰。

`-log-budget 0` 的控制端也会请求重试；要禁用输出采集，应同时设置 Agent 的 `-log-budget 0`。

## 验证范围

在现场验证停机处置、挂载枚举和故障恢复，见 [Linux 验收清单](linux-deployment-acceptance.md)。

## 取消部署

在主服务数据目录记录取消请求。

```sh
moori cancel-deployment -id DEPLOYMENT_ID \
  -operator OPERATOR -note "reason for stopping this deployment"
```

提交部署的 CI 凭证可调用 `POST /api/v1/deployments/{id}/cancel`，无需请求体。未派发的部署在事务内取消。已派发时，HTTP 202 表示取消请求已记录，最终结果由部署状态接口给出。

Agent 收到取消请求后，尚未启动的任务可记录为取消；已经启动的任务由 Agent 通知对应工作进程停止。如果 Docker 已接收变更且结果不明，部署进入核查，目标保持锁定。已确认成功的任务保持原状态。

## 回滚部署

```sh
moori rollback-deployment -from KNOWN_GOOD_DEPLOYMENT \
  -key INCIDENT_ROLLBACK_1 -operator OPERATOR -note "why this release is being restored"
```

提交历史成功部署的同一 CI 凭证可调用 `POST /api/v1/deployments/{id}/rollback`。发送 JSON `{"note":"原因"}` 和 `Idempotency-Key` 后，查询响应中的新部署 ID。本地运维命令可以回滚早先其他凭证创建的成功部署。

回滚在 `rollback_of` 记录来源，并复用源部署的 Compose 快照、镜像摘要和配置版本。通过权限和快照校验后，回滚直接进入队列。相同请求重试返回同一个回滚部署 ID。卷和外部数据库数据按各自的备份恢复流程处理。

## 部署输出

主服务默认保留 64 MiB 部署输出，Agent 默认暂存 16 MiB 待上传日志。目录默认是各自 SQLite 路径加 `.logs`，可用 `-log-dir` 更改位置，或用 `-log-budget` 指定字节额度。目录应仅供对应进程访问。超出字节或文件数量限额时清理旧文件。

- 提交部署的 CI 凭证可调用 `GET /api/v1/deployments/{id}/logs`。
- 有权限的项目成员可携带会话 Cookie 调用 `GET /api/v1/projects/{project}/deployments/{id}/logs`。

响应包含 `text` 和 `truncated`。日志在执行后尽力上传，缺失时可检查执行是否结束、是否有输出以及日志目录和额度。执行结果走独立的持久化协议。Compose 输出可能含应用或仓库信息，应按敏感数据处理。日志文件单独备份。
