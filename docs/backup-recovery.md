# 备份与恢复

主数据库备份不包含本机执行账本、远程 Agent 账本、部署日志、健康数据库、加密主密钥和应用卷数据，需要分别保管。不能直接复制运行中的 SQLite 主文件，也不能将备份文件直接作为运行库启动。

本机执行账本默认为 `moori.db.local.db`。停主服务后，保留该文件及存在的 `-wal`、`-shm` 附属文件，以及日志和仍被容器挂载的密钥目录。恢复主库到不同文件名时，用 `serve -local-db /原路径/moori.db.local.db` 继续使用原账本，并保留原 `-local-secret-dir`。已有本机身份但账本丢失时，主服务拒绝创建空账本继续执行。

## 创建一致性备份

从[运行实例的数据目录](deployment.md#数据目录)执行本页的主服务 CLI 命令。

```bash
mkdir -p backups
moori backup-db -out backups/control-backup.db
moori verify-backup -file backups/control-backup.db
```

输出必须是新的文件路径，所在本地文件系统需支持硬链接。备份读取 SQLite 已提交的数据，包括 WAL 中已提交的变更；完成数据库身份、版本、完整性和外键检查后才发布文件。

备份文件包含用户密码哈希、会话哈希、部署配置、加密后的 Registry 凭证、CI 令牌哈希与目标授权、独立容器目标与请求记录、Webhook 绑定与凭证哈希，以及审计信息，需限制访问。用于解密部署密钥与 Registry 凭证的主密钥和 Gitea Webhook 配置单独保存。节点监控数据库和日志分别保管。

## 准备恢复

先停止旧主服务，避免两个实例同时控制节点。使用新路径准备恢复数据库：

```bash
moori prepare-restore -file backups/control-backup.db -out control-recovery.db
moori restore-status -db control-recovery.db
```

该操作保留备份原件，并创建新的 recovery epoch。备份中的用户会话、CI token 和 Agent 凭证失效；未派发部署被取消，已经派发且结果不确定的工作保留目标锁。目标未释放前，新的提交和派发被拒绝。

独立容器 Webhook 的绑定和撤销状态回到备份时点。恢复期间，旧的排队节点操作会失效，正在运行的变更需要核对现场。重新开放节点操作前，核对备份后撤销过的 Webhook 绑定，并按需要重新撤销。重新签发多目标 CI 令牌前，核对它的授权范围和独立容器目标的节点代次。[流水线接入](integrations.md#gitea-actions-直接部署独立容器) · [独立容器 Webhook](integrations.md#独立容器更新-webhook)

本机执行器无需重发凭证，启动主服务后自动提交本机账本盘点；只为远程 Agent 重发凭证：

```bash
umask 077
moori issue-agent-credential -db control-recovery.db \
  -agent host-01 > host-01.credential
```

将新凭证放到对应节点的私有凭证文件，启动恢复实例，再重启保留原账本的 Agent。原实例使用 local 时，启动命令必须指向原本机执行账本：

```bash
moori serve -db control-recovery.db -local-db /data/moori/moori.db.local.db
```

替换为原账本的实际路径，并沿用原本机密钥目录参数。纯控制中心使用 `moori serve -db control-recovery.db -local-execution off`。原系统使用部署密钥或私有 Registry 凭证时，启动时仍需加载原加密主密钥。

恢复标记下首次重发保持备份中的 generation，以便原账本参与盘点；后续普通轮换仍增加代次。凭证重发本身不解除目标锁。不要新建空账本代替原节点执行证据。

## 盘点与现场核查

```bash
moori restore-inventory -db control-recovery.db -agent host-01
```

主服务接收待确认事件与保留执行记录的分页盘点，包括恢复前已确认的终态。分页使用 `-after` 和返回的 `next`。完整盘点表示收到了节点保留的账本，不等于 Docker 现场已确认安全；离线节点或已清理的历史可能造成证据缺口。

本机盘点使用 `moori restore-inventory -db control-recovery.db -agent @local`。本机中断执行需要处置时，先停主服务并确认 Docker 现场，再执行 `moori retire-local-execution -local-db /原路径/moori.db.local.db -execution OLD_EXECUTION -operator OPERATOR -note "现场核查依据"`。重启主服务后继续下方逐目标恢复流程；本机处置不需要安装 Agent。

检查目标的 Compose project、容器、镜像，以及仍可能执行变更的进程和 Docker 操作。如果账本存在中断执行，先停止 Agent，确认现场没有旧操作，再进入 Agent 的[数据目录](deployment.md#数据目录)，明确指定要处置的账本。

```bash
moori-agent retire-execution -db agent.db -execution OLD_EXECUTION \
  -operator OPERATOR -note "现场检查结果以及确认旧操作已停止的依据"
```

该命令独占账本，只接受已处于核查状态的执行，写入 `abandoned` 结果，保留原结果未知这一事实。重启 Agent，让结果与盘点重新上报。

## 逐目标恢复服务

从 `restore-status` 读取当前 epoch，仅对已核查目标执行：

```bash
moori restore-release-target -db control-recovery.db \
  -epoch RESTORE_EPOCH -project team -target prod \
  -operator OPERATOR -note "已核对容器并确认没有旧部署操作运行"
```

释放要求当前绑定 Agent 完成盘点、目标没有最新活动执行证据，且占槽的历史 offer 已过期。无法归属到目标的活动证据也会阻止释放。如果备份保留的是同一个 Agent 的旧代次，先用当前凭证完成盘点，并在释放说明中记录对旧节点和执行的核查结果。检查通过后，释放操作会在同一事务更新目标的绑定代次。

释放清理不可信的历史成功指针，并将未决尝试记录为 `abandoned`。未释放目标继续锁定；旧事件仍作为恢复证据，新部署使用当前 epoch。重新签发 CI token 后可向已释放的目标提交新部署，成功后重建目标成功版本指针。

逐目标释放即可恢复该目标的部署提交和派发。恢复时已有节点绑定或执行锁的目标需要释放，可从 `restore-status` 的 `pending_targets` 查看剩余数量。全部释放后，主服务恢复新目标接入、Registry 修改和节点 Docker 管理。恢复代次继续用于区分旧回报和新工作。旧库中尚未领取的 Docker 操作不会延后派发。恢复前已领取的变更操作可能仍在 Docker 侧运行，在原领取时限内会继续阻止该节点上的新变更工作。迟到的旧执行回报仍显示活动时，该 Agent 的 Docker 变更也会暂缓，直到同一代次的执行证据有了明确终态。恢复期间如需轮换凭证，先在原账本中处置已记录的活动执行并确认终态已上报。已经失去原代次回报能力且仍有活动证据时，当前流程会继续保留隔离。
