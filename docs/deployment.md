# 部署指南

主服务为 `moori`，可直接部署本机 Docker，无需单独安装 Agent。其他机器运行 `moori-agent`，主动连接主服务。下列命令在 Linux 上执行，示例使用可信内网中的主服务地址直连。需持续保留主库、本机执行账本及远程 Agent 的数据目录。

主服务首次启动会创建 `moori.db` 及其初始结构。

## 数据目录

| 程序 | 直接运行时的工作目录示例 | 容器内默认数据目录 | 默认数据库 |
| --- | --- | --- | --- |
| `moori` | `/data/moori` | `/data` | `moori.db` |
| `moori-agent` | `/data/moori-agent` | `/data` | `agent.db` |

本机执行账本默认为 `<主库路径>.local.db`，主服务容器内为 `/data/moori.db.local.db`；可以通过 `serve -local-db` 指定原账本。不要删除或用空账本替换已有执行记录。

直接运行时，数据库默认位于当前工作目录。容器镜像已通过 `MOORI_DATA_DIR` 设置表中的数据目录，启动和管理命令都从挂载中打开数据库，工作目录的变化不影响默认数据库。原生部署也可用该环境变量指定数据目录。恢复独立数据库或处置特定执行账本时，按[备份与恢复](backup-recovery.md)指定文件。

## 主服务

使用 `make build && sudo make install` 安装程序，然后启动：

```bash
sudo install -d -m 0700 -o "$(id -u)" -g "$(id -g)" /data/moori
cd /data/moori
moori serve
```

首次打开网页会显示“欢迎使用 Moori”。设置管理员账号、密码和确认密码后，程序自动创建平台管理员并登录，无需进入终端创建用户或授权。账号默认填入 `admin`，可自行修改；密码至少 12 个字符。

首次访问者可完成初始化，应先在受控网络完成设置，再开放访问。初始化只能成功一次，并发提交不会创建多个初始管理员。已有用户的数据库直接显示登录页，不覆盖现有账号。登录后进入“账户设置”，验证当前密码即可修改账号和密码；保存后所有旧会话失效，使用新凭据重新登录。

### 创建本机部署目标

在「运行环境」可查看自动接入的「本机 Docker」。在「系统设置 → 流水线接入 → 部署记录」点击「创建部署目标」展开表单。选择「本机 Docker」后填写项目和目标名称即可创建。

主服务需要访问 `/var/run/docker.sock`，系统需提供 Docker CLI 和 Compose 插件。默认自动检测并接入本机，示例 Compose 无需额外启动参数。页面会显示 `local` 是否可用。需要显式控制时，可使用 `-local-execution on` 要求本机执行可用，否则启动报错；`off` 用于仅管理远程机器。

平台管理员可以在控制台「运行环境」选择「本机 Docker」或任意已接入的 Agent，查看系统和硬件、文件系统、Docker 配置、容器用量、Compose 项目、镜像、网络和卷。采集随节点启动，运行期间周期更新。发现已有服务后可直接查看资源。

容器示例把宿主机的 `/proc`、`/sys`、`/etc/os-release` 和 `/etc/hostname` 分别只读挂到 `/host` 下，并通过 `MOORI_HOST_ROOT=/host` 指定采集路径。原生 Linux 进程直接采集宿主机。容器可以枚举宿主机文件系统的挂载点，容量仅在节点能访问对应文件系统时显示。若需监控额外的文件系统，将该挂载点单独只读挂到 `/host` 下的对应位置，例如 `/srv/storage:/host/srv/storage:ro`。

创建目标后，平台管理员可在「系统设置 → Compose 模板」选择模板版本创建部署。也可以通过 CI/API 或 `publish-release` 发布版本，再到项目中选择目标和版本发起部署。详见 [Docker 管理](docker-management.md#compose-模板) 和 [CI 接入](integrations.md)。创建目标只保存配置。

### 在网页接入远程机器

平台管理员在「运行环境」点击「创建 Agent Token」，填写节点标识并生成 Token。保存页面上一次性显示的 Token。主库只保存哈希。请求中断时，在同一表单内短时重试可找回本次签发结果。若 Token 已丢失，可通过下述 CLI 轮换。

在远程机器设置 `MOORI_AGENT_TOKEN` 和从该机器可达的 `MOORI_ENDPOINT`，启动 `moori-agent` 后由它主动连接主服务。也可下载仅引用这两个变量的 Compose 配置。连接成功后，在「系统设置 → 流水线接入 → 部署记录」创建部署目标并选择该远程 Agent。同一 Agent 可绑定多个部署目标。

`-addr` 默认监听所有 IPv4 接口。浏览器使用 `http://主服务地址:8080`，Agent 使用 `ws://主服务地址:8080/agent/v1/connect`。两者可以直接访问同一个端口。仅供本机使用时，可设置 `-addr 127.0.0.1:8080`。HTTP 和 WS 本身不加密，直连应限定在可信网络。

健康检查为 `GET /healthz`，会检查主数据库可用性。日志目录 `moori.db.logs` 和健康库 `moori.db.metrics` 默认与主库放在一起。主服务自动装载或首次创建加密主密钥，见[密钥管理](secrets.md)。

## Agent Token

也可以在主服务主机的数据目录中通过 CLI 签发：

```bash
umask 077
moori issue-agent-credential -agent node-1 > node-1.credential
```

标准输出只有 Token，诊断输出提供 Agent generation。默认有效期一年，CLI 可用 `-ttl` 调整，最长十年。

将控制台签发的 43 个字符原样填入 `MOORI_AGENT_TOKEN`。已接入的 Agent 继续使用原有完整 Token。

也可将 Token 放在节点运行用户拥有、权限为 0600 的私有文件中，通过 `-credential-file` 加载。使用此方式时，省略 `MOORI_AGENT_TOKEN`。节点用户需能访问 Docker socket 并写入数据目录。

```bash
sudo install -d -m 0700 -o "$(id -u)" -g "$(id -g)" /data/moori-agent
# 将签发的凭证放入上述目录后执行：
cd /data/moori-agent
chmod 0600 credential
moori-agent -endpoint ws://moori.internal:8080/agent/v1/connect -credential-file credential
```

将 `moori.internal:8080` 替换为节点实际可达的主服务 IP 或域名及端口。Agent 默认在当前目录保存 `agent.db`、`agent.db.logs` 和 `agent.db.secrets`。每份凭证和账本仅运行一个 Agent 进程。

## 绑定部署目标

首次签发的 generation 为 1。在主服务的数据目录中使用命令实际输出的代次注册目标：

```bash
moori register-target -project team -target test -agent node-1 -generation 1
```

发布 Compose 与触发部署见 [CI 接入](integrations.md)，用户授权见 [权限说明](access.md)。

再次为同一 Agent 签发凭证会增加 generation，旧凭证与旧连接失效。轮换时停止旧节点、替换凭证、重启节点，核查旧执行后使用 `assign-target` 更新空闲目标的绑定代次。凭证轮换不会解除执行锁，也不证明旧 Docker 工作已停止。

撤销凭证：

```bash
moori revoke-agent-credential -agent node-1
```

撤销阻止后续通信，不会远程撤回已经运行的 Docker 操作。恢复数据库后的首次凭证重发遵循专门规则，见 [备份恢复](backup-recovery.md)。

## 容器部署

在仓库根目录构建：

```bash
docker build -f deploy/Dockerfile -t moori:latest .
docker build -f deploy/Dockerfile.agent -t moori-agent:latest .
```

主服务使用 `deploy/compose.example.yml`，只需这一个服务即可部署本机应用。主服务镜像包含 Docker CLI 和 Compose 插件。服务在容器内监听 `0.0.0.0:8080`。宿主端口默认发布到 `0.0.0.0:8080`，浏览器和 Agent 可使用可达的宿主地址直连。通过 `MOORI_BIND_IP` 可限定为私网地址或回环地址。首次初始化前限制端口访问范围。

主服务镜像默认执行 `moori serve`，默认以 root 运行，以访问宿主 Docker socket，无需在 Compose 额外设置用户。示例使用 `moori-data:/data` 持久化主库、本机账本、指标库和日志，同时挂载 Docker socket 以及前述只读宿主机信息。仅管理远程机器时，可使用[轻量主服务镜像](release.md#镜像)，或设置 `-local-execution off`、移除 Docker socket 挂载，并在确认数据目录可写后指定 `user: "10001:10001"`。Docker socket 访问权限意味着可以管理宿主 Docker。

如果 Docker socket 提示权限不足，先核对容器的运行用户和 socket 的属组。使用普通用户运行 local 时，该用户需要具备 socket 的组访问权限，并能写入 `/data`。纯控制中心模式可以取消 Docker socket 和宿主机采集挂载。

只有本机应用使用 Moori 管理的文件密钥时，才需要额外配置密钥文件目录。按 [密钥管理](secrets.md) 配置主密钥后，为主服务增加 `-local-secret-dir /data/moori-local/secrets`，并增加 `/data/moori-local:/data/moori-local` 挂载。此时宿主与容器路径必须一致，因为密钥文件由主服务写入、由宿主 Docker 挂载到应用容器；`moori-data:/data` 的容器路径不能直接作为宿主文件路径使用。

下列命令均从仓库根目录执行，并显式指定 Compose 文件：

```bash
docker compose -f deploy/compose.example.yml up -d
# 登录后在「系统设置 → 流水线接入 → 部署记录」创建本机部署目标。
```

只有管理其他机器时才启动 Agent。使用网页下载的配置，或使用 `deploy/compose.agent.example.yml`。在远程主机为 Compose 准备权限为 0600 的 `.env`，填入实际连接地址和 Token：

```dotenv
MOORI_ENDPOINT=ws://moori.internal:8080/agent/v1/connect
MOORI_AGENT_TOKEN=<控制台创建的 Token>
```

```bash
docker compose --env-file .env -f deploy/compose.agent.example.yml up -d
```

Agent 容器默认以 root 访问宿主 Docker socket。示例通过独立命名卷 `moori-agent-data:/data` 保存账本和日志。已有宿主目录的节点可使用 `/data/moori-agent:/data`，重建后继续使用原有的 `agent.db`。

使用 Moori 文件型部署密钥时，可将专用密钥目录按相同绝对路径挂给 Agent，例如 `/srv/moori-secrets:/srv/moori-secrets`，并设置 `command: ["-secret-dir", "/srv/moori-secrets"]`。这样宿主 Docker 可以按 Agent 写入的路径读取文件。已有部署使用旧密钥目录时，保留其宿主路径，并将该目录按原路径挂载，启动和运行 `gc-secrets` 时均用 `-secret-dir` 指向该位置。

## SQLite 文件打不开

启动日志会给出数据库文件的绝对路径。主服务容器通常打开 `/data/moori.db`，Agent 容器通常打开 `/data/agent.db`。日志显示 `/moori.db` 时，检查容器是否保留了镜像默认的 `MOORI_DATA_DIR=/data`。日志显示 `/data/moori.db` 时，检查 `/data` 的实际挂载。Compose 可在服务启动失败时使用 `ps -aq` 找到容器。

```bash
docker inspect "$(docker compose -f deploy/compose.example.yml ps -aq moori)" \
  --format 'workdir={{.Config.WorkingDir}} cmd={{json .Config.Cmd}} mounts={{json .Mounts}}'
```

如果日志报父目录不存在，检查数据库路径与挂载目标是否一致。如果数据库路径是目录，检查是否把宿主机目录挂到了数据库文件名上。如果提示写权限，检查挂载是否允许写入，以及数据库和所属目录是否可由运行用户写入。主服务镜像默认以 root 运行。覆盖容器用户时，按实际 UID/GID 检查宿主挂载目录的所有者和权限。

## 控制台网关

已有网关可选用 TLS 终止来提供加密访问。证书由网关管理，Moori 继续提供 HTTP 服务。网关与主服务部署在同一宿主机时，可使用回环端口或共享容器网络。

现有 HTTPS 网关保留浏览器的 `Origin` 和 `Sec-Fetch-Site`。现代浏览器提供同源标头时，控制台可在代理改写后端 `Host` 的情况下访问。保留原始 `Host` 和端口可兼容缺少该标头的浏览器。Agent 路由还需保留 `Authorization` 和 WebSocket 升级，代理空闲超时至少 90 秒。

需要信任转发协议头时，例如为远程 Agent 下发私有 Registry 凭证，在 Moori 进程或 Compose 环境中设置 `MOORI_TRUSTED_PROXY_CIDRS`，填写 Moori 实际看到的直连代理地址。宿主机进程间回环转发可使用 `127.0.0.1/32`。网关需覆盖客户端传入的 `X-Forwarded-Proto`，并限制后端端口仅供网关访问。

[Nginx 示例](../deploy/nginx.example.conf) 使用 `https://moori.example.com` 作为浏览器入口，Agent 使用 `wss://moori.example.com/agent/v1/connect`。将示例中的域名、证书路径和上游地址换成实际值。通用请求体上限为 2 MiB；浏览器镜像导入和 Agent 档案上传路径单独使用 8 GiB 上限及流式转发。已有网关请同步更新两条路径，并用实际传输速率核对超时。[Nginx 请求体配置](https://nginx.org/en/docs/http/ngx_http_core_module.html#client_max_body_size)

经网关中继实时容器日志时，`/agent/v1/log-stream/` 路径需要逐段转发请求体，容器的 `log-stream` 路径需要逐段转发响应。示例为这两条路径留出了最长 30 分钟的日志流所需的空闲超时。[Nginx 代理缓冲配置](https://nginx.org/en/docs/http/ngx_http_proxy_module.html#proxy_request_buffering)

已有 Traefik 可用带 TLS 的 HTTP 路由转发到 `http://moori:8080`。Agent 使用 `wss://Moori域名/agent/v1/connect`，Traefik 自动处理升级。后端使用 Moori 在 Traefik 网络中可达的地址。[Traefik WebSocket 文档](https://doc.traefik.io/traefik/master/user-guides/websocket/)

远程 Agent 需要从 Moori 领取私有仓库凭证时，另见 [Registry 凭证转发](docker-management.md#registry)。

完成后按 [Linux 验收清单](linux-deployment-acceptance.md) 核对真实部署、断线重连和恢复。
