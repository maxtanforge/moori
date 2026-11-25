# 功能路线图（对标 Portainer）

本文是前瞻性规划，不是使用指南：使用与操作以 [文档索引](README.md) 各篇为准。规划以 2026-11-20 的全流程实测与源码走查为基线，目标是用 Portainer 已验证的「以资源对象为中心」的运维动线，打磨现有功能，而不改变 moori 的安全与发布模型。

## 现状定位

实测确认的强项（保持并继续作为差异化优势）：

- 发布链：快照摘要防漂移、幂等键、回滚（[部署模型与设计](design.md)）。
- 接入模型：Agent 主动外联，无需入站端口；节点代次与凭证轮换。
- 平台底座：审计、备份/恢复 CLI（[备份恢复](backup-recovery.md)）、部署输出落盘可查（[部署运维](deployment-maintenance.md)）。

核心差距是「日常运维动线」：Portainer 以资源对象为中心（面板 → 容器 → 日志/终端 → 操作，数秒完成），moori 以节点工作台与发布流水线为中心，高频动作要么缺失、要么埋得深。

## 对标原则

| # | 原则 | 现状 |
| --- | --- | --- |
| 1 | 左栏按资源类型分页，环境（节点）切换置顶 | 已有：工作台分区 overview/containers/compose/images/networks/volumes/host/logs（`internal/control/web/node-workspace.js`） |
| 2 | 列表即操作台：每行快捷操作 + 批量动作 | 已有：行内启停/删除 + 批量启停（`containerSelection`） |
| 3 | 对象详情多 Tab（概要/日志/统计/规格） | 已有：详情页概要/规格/环境与标签 Tabs，日志与监控为独立路由（`containers/{target}/{logs,monitor}`） |
| 4 | 日志查看器细节：自动刷新/搜索/时间戳/下载 | 已有：实时流 + 快照回退、行过滤、时间戳开关、下载（`internal/transport/logstream`） |
| 5 | 容器终端（exec TTY） | 已有：运行中容器的网页终端，vendored xterm.js 同源加载，本机直连或远程 Agent 转发的 Docker Exec TTY（`internal/transport/execstream`） |
| 6 | 统计实时图表（CPU/内存/网络曲线） | 已有：节点与容器级曲线（`internal/control/web/monitoring.js`，2026-11-21 起以 vendored uPlot 渲染），30s 自动刷新 + 4 档时间范围 |
| 7 | Stack 编辑器 + 存量项目发现纳管 | 已有：preview/apply 链路（`internal/control/human/compose_maintenance.go`）、外部项目源文件编辑，以及源文件页的一键纳管评估（`internal/core/release/adopt.go`，转换结果直接生成部署模板） |
| 8 | 全局仪表盘：跨节点计数 + 健康卡片 | 已有：`GET /api/v1/overview` + 环境目录健康卡片（节点/部署/告警） |

## 阶段规划

### P0 快赢打磨（已完成，2026-11-20）

- 已完成：部署列表 failed/needs_attention 行直达输出日志；容器列表批量启停（确认弹窗 + 逐容器独立操作）；CLI 磨损项（备份输出目录缺失改名报错、Agent 缺 Docker CLI 的启动提示）。
- 说明：走查时误报的「操作历史无 UI」「列表无筛选」实为已有功能（`renderNodeOperationHistory`、容器搜索/状态筛选），未重复建设。

验收：走查脚本（部署一个失败目标 → 在控制台不借助 API 即可从列表直达失败输出；提交一次容器删除 → 操作历史页可见记录与结果）。

### P1 全局仪表盘与导航聚合（已完成，2026-11-20）

- 已完成：`GET /api/v1/overview`（平台管理员）聚合节点 contact 状态、在途部署、24h 审计成功/失败计数、活动告警与窗口触发数（无指标库时 `enabled=false` 降级）；环境目录健康卡片改为读该聚合（节点/部署/告警三卡），分页客户端计数保留为加载期回退。

验收：首次登录一屏回答「现在哪个节点不健康、哪些容器停了、有没有告警」，且每张卡可穿透。

### P2 容器运维闭环（已完成，2026-11-20）

- 已完成（2026-11-20）：容器详情页 Tabs（概要/规格/环境与标签，日志与监控为路由入口）；日志增强（行过滤、时间戳开关——`nodeops.Request` 新增 `timestamps` 字段仅限 `container.logs`，随操作队列与实时流工单透传；下载）；实时统计图表（2026-11-20 核对确认 `monitoring.js` makeChart 已覆盖；2026-11-21 按「成熟库优先」改为 vendored uPlot 渲染）。
- 已完成（2026-11-20）：容器终端（exec TTY）。落地形态：浏览器 WebSocket（会话 Cookie + 平台管理员）→ 控制面 `internal/transport/execstream` 配对 broker（逐帧零缓冲转发、全服务并发上限 8、ping/pong 保活与超时清理）→ Agent WSS 专线 `/agent/v1/exec-stream`（凭证鉴权 + 强制证书验证）→ Agent 侧 `docker exec` TTY hijack（bash 优先 sh 回退、TERM=xterm、resize 走 daemon）。浏览器侧 vendored xterm.js + fit addon 同源加载满足 CSP（`internal/control/web/terminal.js`），面板尺寸变化即发 resize；会话开始/结束写入平台审计。本机容器终端通过主服务直连 Docker socket，沿用相同的浏览器协议、权限和审计。入口仅对运行中的容器显示。

验收：从容器列表到「看日志 30 秒、进终端重启进程、看 CPU 曲线」全程不离开控制台。

### P3 Compose/Stack 体验（存量项目接管，已完成，2026-11-20）

- 已完成：外部项目展示与只读回读（`localinv.Overview` 采集 `compose_projects`，授权源文件回读与源文件编辑此前已落地，见 [Compose 支持范围](compose-support.md)）。
- 已完成（2026-11-20）：一键纳管。源文件可读的单文件外部项目在源文件页执行「评估纳管」：控制台把该文件逐服务转换为受管子集（`internal/core/release/adopt.go`，每个简化都生成明确说明，不静默丢弃），镜像按节点快照的 RepoDigests 固定（部署 `--pull never`，无需 registry 访问），转换结果经发布编译自检后保存为部署模板；随后走标准模板部署（把已有卷和网络绑定回原名复用现有数据），核对正常后删除原项目。
- 已完成：Stack 编辑器串链（源文件页 编辑 → `compose.source-preview` 校验预览 → 保存/部署；受管项目 preview/apply 链路此前已有）。

验收：一台装有既有 Compose 项目的机器接入后，控制台可见项目列表、可查看生效配置、纳管后可从控制台更新。

### P4 观测与告警增强（已完成，2026-11-20）

- 已完成：告警策略迁为控制台可配置并持久化（系统设置 → 告警通知，`alert_settings`/`alert_targets` 表，`/api/v1/alert-settings`、`/api/v1/alert-targets`）；评估循环每周期重读策略，不重启即生效；通知目标支持最多 10 个 webhook，投递向全部目标扇出并共用事件 `Idempotency-Key`（部分失败整轮重试）；环境变量降级为初始值（阈值仅在控制台未自定义时生效，`MOORI_ALERT_WEBHOOK` 在目标为空时导入一次）。历史曲线页此前已具备（`monitoring.js`，1h/6h/24h/7d）。

验收：不重启服务即可调整告警阈值并收到通知（实测：保存阈值、增删目标后评估循环与投递即时采用）；任一节点可查看 1h/6h/24h/7d 曲线。

### P5 平台能力（远期）

- 团队与 RBAC 细化、OAuth/LDAP 接入。
- 内置模板集：模板参数化机制已支持 ports/environment/volumes/networks（`console_templates.go`）。
- 镜像构建（`docker build`）与主机文件浏览（只读）。

## 决策点汇总

1. P3 存量项目纳管（8b79077 裁撤项的只读半边）已决策实施并于 2026-11-20 落地，见 P3。
2. 后续顺序：P0/P1/P2/P3/P4 已全部落地，剩余远期项见 P5。

## 附：基线实测记录（2026-11-20）

控制面从零走通：初始化 → 登录 → 签发 Agent Token → Agent 上线（contact=online、主机指标实时回传）→ 目标/模板/预览/部署（幂等键）→ 失败输出落盘 → 节点操作 queued→failed 闭环 → 成员权限 403 门禁 → 审计落库 → `backup-db`/`verify-backup`/`prepare-restore`/`restore-status` 全通过。环境无 Docker：Agent 端在无 Docker CLI 时启动即退出（`exec.LookPath`，`cmd/moori-agent/main_linux.go`），库存照报主机与文件系统、Docker 段降级提示。
