# CI 与 Gitea 接入

CI 构建并推送镜像，Moori 接收镜像摘要并在选定节点执行部署。在 Gitea 仓库的 `.gitea/workflows/` 中定义流水线，通过 `runs-on` 选择带对应标签的已注册 Runner。Moori 的发布与部署接口也适用于其他 CI。[Gitea Actions](https://docs.gitea.com/usage/actions/quickstart/) · [Runner 标签](https://docs.gitea.com/runner/labels/)

| 部署对象 | 流水线提交方式 | 查询结果 |
| --- | --- | --- |
| Moori 受管 Compose | 先发布版本，再调用部署 API，或用同名 Gitea Release 触发 | 部署 ID |
| 独立容器 | 推送镜像后，使用目标范围内的 CI Token 调用容器部署 API；也可单独使用 Webhook | 节点操作 ID |

以下管理命令在主服务的[数据目录](deployment.md#数据目录)中运行。`-compose` 指定的文件需能从执行命令的环境访问。

## 控制台统一接入

在「系统设置 → 流水线接入」按应用管理部署环境，独立容器和 Compose 共用同一入口。已有目标自动归入所属应用，无需重新创建令牌或修改现有 CI。

API 中的 `project` 对应应用，`target` 对应部署环境；现有接口和权限范围保持兼容。

1. 选择应用，再选择部署环境；也可以「接入应用」或给已有应用添加环境。环境绑定执行节点，节点凭证代次由页面自动读取。环境标识在平台内唯一，建议使用 `应用名-production`、`应用名-staging`。
2. 选择独立容器或 Compose。独立容器绑定当前容器名及镜像仓库；Compose 创建受管部署环境，定义文件随应用代码保存在 Git 中，不会自动接管外部 Compose 项目。
3. 在环境详情签发接入凭证，保存为 CI Secret `MOORI_TOKEN`；`MOORI_URL` 配置为 Runner 可访问的主服务地址。Compose 的默认凭证包含提交版本的权限，也可取消该权限，仅部署已发布版本。额外环境仍须逐项授权。
4. 选择 Gitea Actions、GitHub Actions 或其他 CI（Shell），复制页面生成的部署步骤，放在镜像构建和推送之后。Moori 接收固定摘要，执行部署并记录结果。两种部署方式均在环境详情查看发布记录。

Actions 构建步骤通过 `GITHUB_ENV` 提供 `MOORI_IMAGE=仓库/镜像@sha256:摘要`。其他 CI 设置同名环境变量，并设置稳定的 `MOORI_DELIVERY_KEY`。重复请求保持原键；新版本或回滚使用新键。生成步骤会等待最终结果，失败或超时使 CI 失败，超时不会取消已受理的部署。

Compose 默认读取仓库中的 `compose.yaml`，用 `MOORI_COMPOSE_FILE` 更改路径。单服务使用 `MOORI_SERVICE`（默认 `web`）与 `MOORI_IMAGE`；多服务设置 `MOORI_IMAGES`，例如 `{"web":"registry.example/app@sha256:…","worker":"registry.example/worker@sha256:…"}`。未覆盖的服务也必须使用固定摘要，配置须符合 [Compose 支持范围](compose-support.md)。页面生成的步骤直接提交 YAML 和覆盖映射，无需先通过 CLI 发布模板；已有模板 API 继续可用。

回滚时，独立容器提交旧镜像摘要；Compose 提交 Git 中的原配置及原镜像摘要，或使用已有部署回滚流程。独立 Webhook 和 Gitea Release Webhook 属于可选接入方式，常规 CI 无需配置它们。

## 由 Git 触发部署

### Gitea Actions 直接部署 Compose

复制 [Gitea 工作流示例](../deploy/gitea-compose-workflow.example.yml) 到你要部署的应用仓库的 `.gitea/workflows/deploy.yml`。这份文件在 Moori 源码仓库里只是示例，不会触发工作流或升级 Moori 自身。应用仓库的 Git 推送会触发 Runner 调用下方的发布和部署 API，无需创建 Webhook。

1. 在 Moori 注册目标并发布 CI 模板。示例模板 ID 为 `web-v1`，允许替换 `web` 服务的镜像；如果模板开放了多个服务，发布请求须提供全部对应镜像。
2. 在「系统设置 → 流水线接入」打开 Compose 目标并签发令牌，勾选「允许发布镜像版本」。把明文保存到应用仓库的 Actions Secrets `MOORI_TOKEN`。设置 `MOORI_URL`、镜像仓库的 `REGISTRY_USER` 和 `REGISTRY_PASSWORD`，再修改工作流中的镜像路径、模板、服务和目标。Runner 需要 Docker Buildx、curl、jq，并能访问镜像仓库和 Moori；Moori 根据部署镜像的注册表域名和仓库路径匹配共享拉取凭证，所有执行节点共用。
3. 流水线推送镜像后，读取刚推送的摘要并提交 Moori。终态失败会使流水线失败。示例流水线最多等待十分钟，超时后部署继续由 Moori 调度。

这个示例用于受管 Compose。独立容器可使用[容器部署 API](#gitea-actions-直接部署独立容器)。Moori 不要求 Gitea 提供专用 API。

### 发布和触发方式

把 Compose 定义或独立容器的目标镜像版本随代码保存，在合并或发布后由 Gitea Actions 触发。流水线检出对应提交、推送镜像并拿到摘要，然后发布受管 Compose 版本或调用容器部署 API。Moori 执行后，流水线使用部署 ID 或操作 ID 查询最终结果。回退时可以在 Git 中还原选定版本，再由流水线提交原镜像摘要。

此接入方式由流水线推送变更。周期拉取 Git 和持续对账由外部系统负责。

一条流水线可以用同一个 `MOORI_TOKEN` 发布到多个项目、多台机器。每台机器仍有自己的部署目标，可以是本机 Docker，也可以是远程 Agent。在「系统设置 → 流水线接入」签发令牌时，打开「授权其他部署目标」并选择允许的目标。工作流按需提交 `project` 和 `target`，也可用矩阵传入 `MOORI_PROJECT`、`MOORI_TARGET`；无需为每个环境创建不同的 Secret。

各目标分别授权；Compose 可以按目标额外授予发布权限。对不同目标使用同一个幂等键会分别记录请求。同一目标内重试时保持原键。撤销令牌会撤销它在所有目标上的权限。可以并行发布，也可以等第一台成功后再提交下一台；各目标返回独立结果，Moori 不做跨节点统一回滚。

### Gitea Actions 直接部署独立容器

1. 从独立容器详情打开流水线配置，或进入「系统设置 → 流水线接入」，新建独立容器目标。目标绑定节点、当前凭证代次、Docker 容器名和完整镜像仓库路径。只允许更新该容器、该仓库内的镜像摘要。各节点共用已登记的镜像仓库凭证。可让 Moori 根据注册表域名和仓库路径自动匹配，也可在创建目标时指定匹配的凭证。
2. 在目标详情签发 CI Token，可一次选择多个项目和目标。将仅显示一次的明文存到应用仓库的 Gitea Actions Secrets，名称用 `MOORI_TOKEN`。配置 `MOORI_URL` 并复制页面上的工作流配置。独立容器只需要部署权限。
3. 构建并推送镜像后，向 `POST /api/v1/container-deployments` 提交 `{"project":"team","target":"production","image":"registry.example/team/app@sha256:<摘要>"}`。请求使用 `Authorization: Bearer <MOORI_TOKEN>`、`Content-Type: application/json` 和 `Idempotency-Key`。使用同一凭证和同一键重试同一目标，会返回原操作；如果改了镜像摘要，请使用新键。
4. 使用同一凭证调用 `GET /api/v1/container-deployments/{id}` 查询状态，也可以在目标详情查看最近运行记录、镜像摘要和错误。`succeeded` 为成功，`failed` 为失败，`uncertain` 时先核对节点 Docker 的实际状态。无需开启 Webhook。

通过 CLI 注册容器环境时，可将已登记的仓库 ID 设为 `MOORI_REGISTRY_ID`。省略 `-registry` 时自动匹配。

```sh
moori register-container-target -project team -target production -agent host-01 -generation 2 -container web -repository registry.example/team/app -registry "$MOORI_REGISTRY_ID"
```

独立容器目标不能用来发布或执行 Compose。节点凭证换到新代次后，旧目标不会自动转到新代次。为新代次创建目标后，先签发并替换流水线 Token，核对它包含仍需使用的其他目标，再撤销旧 Token。

## Compose 命令行备选流程

CI 可以部署到已创建的 local 或远程目标。使用已有目标可直接跳过注册。通过 CLI 注册远程目标时使用 Agent 的当前代次，以签发命令输出为准。启用节点监控时也可通过 `moori agent-health -agent host-01` 查询。以下示例使用代次 `2`。[节点接入](deployment.md)

```sh
moori register-target -project team -target test -agent host-01 -generation 2
```

发布版本并签发单目标 CI 凭证。需要一次授权多个目标时，使用控制台签发：

```sh
moori publish-release -project team -release r1 -compose compose.yaml
moori issue-ci-token -id build-test-1 -project team -target test -ttl 24h
```

最后一条命令只输出一次 token。将其保存在 CI 密钥中，请求时使用 `Authorization: Bearer <token>`。向 `POST /api/v1/deployments` 发送 `Content-Type: application/json`、重试时保持相同的 `Idempotency-Key` 和请求体 `{"project":"team","target":"test","release":"r1"}`。HTTP 202 返回部署 ID；同一凭证可通过 `GET /api/v1/deployments/{id}` 查询自己提交的部署。

使用 `moori revoke-ci-token -id build-test-1` 撤销凭证。派发时发现排队部署的凭证过期或已撤销，调度器会将其置为失败。已发出且未确认的执行保留目标锁等待核查，已确认受理的执行继续运行。部署绑定当前目标配置，默认健康等待时间为两分钟。

## 发布 API 与模板

使用 `moori issue-ci-token -id ci-release -project team -target test -allow-publish` 签发带发布权限的目标级凭证。

向 `POST /api/v1/releases` 发送 Bearer 凭证及以下一种请求体。

```json
{"project":"team","target":"test","id":"v1","compose":"<Compose YAML with digest-pinned images>"}
```

```json
{"project":"team","target":"test","id":"v2","template":"base-v1","images":{"web":"registry.example/web@sha256:<digest>"}}
```

使用 `moori publish-template -project team -target test -template base-v1 -compose base.yml -allow-images web` 发布 CI 模板。CI 请求需要提供清单内每个服务的镜像；其他字段与镜像沿用模板。控制台模板库按自己的版本和部署流程使用。[控制台模板](docker-management.md#compose-模板)

发布返回不可变的版本 ID 和摘要。同一目标上，编译内容相同时可以复用 ID，内容变化时返回冲突。使用 `GET /api/v1/projects/{project}/targets/{target}/releases/{release}` 查询版本，并通过部署 API 提交。目标专属版本优先于同名项目版本。通过 CLI 发布的项目版本在项目内共享。

## 独立容器更新 Webhook

平台管理员在「系统设置 → Webhook 管理」创建、查看和撤销独立容器绑定，也可从独立容器详情打开配置表单。创建后复制调用地址和一次性 token。后台管理接口使用控制台会话。

绑定使用节点及当前凭证代次、Docker 容器名和完整镜像仓库路径。容器重建后仍沿用相同的名称。本机使用 `@local` 和代次 `1`，远程节点的当前代次可通过[节点管理接口](access.md#节点管理接口)查询。远程节点换到新代次后，需要创建对应代次的绑定。

```json
{
  "node": "@local",
  "generation": 1,
  "container": "web",
  "repository": "registry.example/team/web"
}
```

拉取私有镜像时，可在上述请求中添加 `registry_id` 指定共享凭证，也可省略以自动匹配。匹配要求注册表域名相同，仓库路径符合登记的前缀。Webhook 只接受上述 `repository` 的固定摘要。[共享 Registry 凭证](docker-management.md#registry)

| 管理请求 | 行为 |
| --- | --- |
| `POST /api/v1/container-webhooks` | 返回 HTTP 201、绑定 `id` 和仅在创建响应中显示的 `token` |
| `GET /api/v1/container-webhooks` | 列出未撤销的绑定，使用返回的 `next` 作为 `after` 翻页 |
| `DELETE /api/v1/container-webhooks/{id}` | 撤销绑定及凭证，尚未派发的请求置为失败 |

将 token 保存为流水线密钥。推送完成后，在发布步骤中使用推送结果中的完整引用 `仓库/镜像@sha256:<摘要>` 作为 `IMAGE_REF`，并为该次发布设置稳定的 `DELIVERY_KEY`。

```sh
curl --fail-with-body --silent --show-error \
  --request POST "$MOORI_URL/api/v1/webhooks/containers/$MOORI_CONTAINER_HOOK_ID" \
  --header "Authorization: Bearer $MOORI_CONTAINER_HOOK_TOKEN" \
  --header "Idempotency-Key: $DELIVERY_KEY" \
  --header 'Content-Type: application/json' \
  --data "$(printf '{"image":"%s"}' "$IMAGE_REF")"
```

HTTP 202 表示 Moori 已受理，响应中的 `id` 是操作 ID。用同一个 Bearer token 查询 `GET /api/v1/webhooks/containers/{id}/{operation}`。`queued` 和 `running` 表示仍在处理，`succeeded` 表示完成。`failed` 时查看响应中的 `error`，`uncertain` 时先核对目标 Docker 上的真实状态。更新仅适用于独立容器，执行时还会核对现有容器的镜像仓库。替换沿用当时的容器配置，新容器启动或健康检查失败时尝试恢复旧容器。

同一次请求重试使用原幂等键和摘要，可取回原操作。复用同一键提交不同摘要，或上一次更新仍在处理时提交新版本，会收到 HTTP 409。发布下一版本或回退镜像时使用新幂等键和对应摘要。回退会沿用当前容器配置。

## Gitea 发布 Webhook

使用 `serve -gitea-hooks /path/to/gitea-hooks.json` 启用 Webhook，并限制配置文件访问权限。每个绑定使用独立的随机签名密钥，至少 32 字节。

```json
[
  {
    "id": "production",
    "repository_id": 10,
    "project": "team",
    "target": "production",
    "secret": "<random signing secret of at least 32 bytes>"
  }
]
```

在 Gitea 中创建 JSON POST Webhook，例如 `https://moori.example.com/api/v1/webhooks/gitea/production`。订阅发布事件，设置相同的签名密钥，并将 Authorization 设为 `Bearer <目标范围内的 Moori token>`。使用私网地址时，按 Gitea 实际版本在 `ALLOWED_HOST_LIST` 中允许该 Moori 地址。[Gitea Webhook](https://docs.gitea.com/usage/repository/webhooks/) · [Gitea 配置](https://docs.gitea.com/administration/config-cheat-sheet/#security-security)

先在 Moori 发布固定镜像摘要的 Compose 版本，再在 Gitea 正式发布使用同名 tag 的 Release。成功接收返回 HTTP 202 和部署 ID。若版本尚未发布到 Moori，发布后可在 Gitea 重投。轮换 Bearer 凭证时保留 Webhook ID，以便同一投递保持幂等。要重新部署已经形成终态的任务，使用 CI API 和新的幂等键。回滚已成功的 Compose 部署按[部署回滚](deployment-maintenance.md#回滚部署)操作。
