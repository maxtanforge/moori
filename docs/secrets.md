# 部署密钥管理

密钥版本按项目和目标隔离，加密存储于主服务 SQLite。部署密钥与 Registry 凭证共用主服务的加密主密钥。密钥文件单独保管和备份。部署快照绑定具体密钥版本与服务挂载权限；Agent 取用时校验凭证、执行归属与精确摘要。

主服务默认加载主库旁边的 `moori.key`（主库名不是 `moori.db` 时使用 `<主库路径>.key`），首次启动且数据库尚未使用加密时自动创建。默认 Docker 路径是 `/data/moori.key`。已有密钥存放在其他位置时，可以设置 `MOORI_SECRET_KEY_FILE` 或使用 `serve -secret-key <路径>`；启动参数优先。恢复已有加密数据时必须使用原密钥。

## 配置目标

从[运行实例的数据目录](deployment.md#数据目录)执行本页的主服务 CLI 命令。

```sh
moori put-secret -secret-key /data/moori.key \
  -project team -target prod -name PASSWORD -version v1 < /private/password
moori set-target-secrets -project team -target prod \
  -version config-v1 -file bindings.json
```

`put-secret` 使用主服务加载的同一密钥文件，并按原样读取标准输入，包括末尾的换行。

`bindings.json` 示例

```json
{
  "secrets": {"PASSWORD": "v1"},
  "services": {"web": ["PASSWORD"]}
}
```

部署提交将当前配置版本及挂载授权写入快照。授权服务可从 `/run/secrets/PASSWORD` 读取密钥。如果镜像支持 `*_FILE` 配置，可把该变量指向文件路径。轮换时创建新的密钥版本和配置版本。执行 `moori revoke-secret -project team -target prod -name PASSWORD -version v1` 后，新的部署和回滚无法再使用该版本。已挂载的文件继续按原容器的生命周期管理。

## 节点存储

远程 Agent 为分配给自己的执行领取指定版本的密钥，local 通过进程内通道领取。节点将值写成 Compose 密钥文件，只挂载到获授权的服务。含部署密钥的执行关闭 CLI 日志采集。

远程 Agent 的默认密钥目录为账本路径加 `.secrets`，可通过 `-secret-dir` 和 `-secret-budget` 指定位置和额度。容器化 Agent 配置此目录的挂载方式见[容器部署](deployment.md#容器部署)。默认额度为 64 MiB，最多保留 1024 个密钥包。确认 Compose 从未启动时可以删除临时文件。Docker 可能已经挂载时保留，当节点离线维护且核对挂载后再通过 `moori-agent gc-secrets` 清理。[部署运维](deployment-maintenance.md#密钥文件清理)
