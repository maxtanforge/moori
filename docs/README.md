# Moori 文档

本文档集描述当前交付的 `moori` 和 `moori-agent`。设计、命令与示例以当前源码为准，过程计划与阶段审查不作为使用指南保留。

| 文档 | 内容 |
| --- | --- |
| [系统架构](architecture.md) | 进程边界、代码分层、数据存储、执行链路 |
| [部署模型与设计](design.md) | 发布、维护模式、执行快照、会话和恢复语义 |
| [部署指南](deployment.md) | 数据目录、管理员初始化、Agent 凭证和容器部署 |
| [运行要求](compatibility.md) | 构建工具、运行依赖与升级边界 |
| [发布指南](release.md) | 二进制、镜像和版本产物 |
| [CI 与 Gitea 接入](integrations.md) | 多目标 CI Token、Runner 直连部署、Compose 发布、独立容器版本更新与可选 Webhook |
| [用户与权限](access.md) | 账号锁定、会话、节点与项目权限和审计接口 |
| [Docker 资源管理](docker-management.md) | Registry、节点显示名、Docker 资源和项目配置维护 |
| [Compose 支持范围](compose-support.md) | 受管与外部项目的字段范围、执行参数与健康检查 |
| [密钥管理](secrets.md) | 加密存储、版本绑定、挂载及回收 |
| [部署运维](deployment-maintenance.md) | 取消、回滚、日志、失联处置及保留策略 |
| [备份恢复](backup-recovery.md) | 一致性备份、凭证重发、盘点与逐目标解锁 |
| [节点监控](observability.md) | 主机和容器历史、部署事件和告警通知 |
| [验收入口](acceptance.md) | 验证范围和环境要求 |
| [Linux 验收清单](linux-deployment-acceptance.md) | Docker 与接入链路的现场验证 |
| [功能路线图](roadmap.md) | 对标 Portainer 的运维动线规划（前瞻规划，非使用指南） |
