# 用户与权限

用户会话、CI token 与 Agent 凭证独立。用户密码使用带随机盐的 scrypt，会话 token 以哈希保存。修改密码使现有会话失效。服务一次处理一个密码请求，以减少并发校验的内存峰值。繁忙时返回 HTTP 429，提示 1 秒后重试。登录及修改账号或密码共用账号级失败计数。连续 5 次密码校验失败会锁定账号 15 分钟，重启主服务后仍保留锁定状态。成功登录或保存账号变更后清零，锁定期间拒绝新登录和账号变更写入。登录错误使用统一提示，既有会话按原有效期处理。

## 初始化与角色

先通过仅管理员可访问的入口打开初始化页面，设置管理员账号和密码，再向其他用户开放控制台。程序原子创建用户、平台管理员权限和登录会话；初始化仅可完成一次。已有用户的数据库不会重新开放初始化入口，即使平台管理员权限被撤销。

登录后通过“账户设置”修改账号和密码，必须验证当前密码。新密码留空时仅修改账号；保存后撤销全部旧会话，角色仍绑定原有用户身份。

在对外开放服务前，也可以在主服务的数据目录用 `moori create-user -username 用户名` 从标准输入设置密码，再执行 `moori grant-platform-admin -username 用户名`。忘记管理员密码时可用同一流程创建独立的恢复管理员；原账号的项目角色不会继承。

## 用户管理与密码重置

平台管理员在「系统设置 → 用户管理」查看账号、平台角色和密码锁定状态，也可创建普通账号。按项目分配角色仍在对应项目中完成。管理员通过「账户设置」修改自己的密码。

重置其他账号时，在页面表单中填写管理员自己的当前密码，并输入两次目标账号的新密码。重置成功后，目标账号的旧密码失效，现有会话被注销，密码锁定及失败计数清零。创建和重置记录写入平台审计。

| 请求 | 行为 |
| --- | --- |
| `GET /api/v1/users` | 列出账号，使用返回的 `next` 作为 `after` 翻页 |
| `POST /api/v1/users` | JSON `username`、`password`，创建普通账号并返回 201 |
| `POST /api/v1/users/{user}/password` | `{user}` 使用账号 ID。JSON `current` 为管理员当前密码，`replacement` 为目标账号的新密码。成功返回 204 |

## 角色与授权

项目角色为 `viewer`、`operator`、`project_admin`。查看者可读取部署、日志和健康状态；操作员可部署、取消和回滚；项目管理员另可管理成员和查询项目审计。平台管理员管理平台授权、节点显示名和项目。项目级角色的权限限于获授权的项目。

目标注册后建立所属项目。用户权限变更在后续请求生效；派发前重新检查排队工作的提交者权限，已受理执行仍可回报结果。

## 撤销权限

需要撤销平台管理员权限时，指定要移除的账户。撤销最后一名平台管理员前，先为接任者授权。

```bash
moori revoke-platform-admin -username former-admin
```

## 登录会话

| 请求 | 输入或作用 |
| --- | --- |
| `GET /api/v1/auth/setup` | 查询是否需要首次初始化 |
| `POST /api/v1/auth/setup` | JSON `username`、`password`；仅未初始化且无用户时可用，成功自动登录 |
| `POST /api/v1/auth/account` | 已登录用户提交 `username`、`current`、`replacement`；新密码为空时保留原密码 |
| `POST /api/v1/auth/login` | JSON `username`、`password`，成功后设置会话 Cookie |
| `GET /api/v1/auth/me` | 查询当前身份 |
| `POST /api/v1/auth/logout` | 注销当前会话 |
| `POST /api/v1/auth/password` | JSON `current`、`replacement`，修改密码并使已有会话失效 |

后续请求携带会话 Cookie。Cookie 只属于当前主机。会话写请求使用 Go 标准库的浏览器跨站保护。浏览器提供 `Sec-Fetch-Site` 时按该标头判断。缺少该标头但提供 `Origin` 时，标准库比较来源和请求的主机及端口。控制台支持 HTTP 直连，账号和会话通过普通 HTTP 传输。HTTPS 页面使用 Secure Cookie，网关应保留浏览器的来源和请求上下文标头。详见[可选网关配置](deployment.md#控制台网关)。自动化发布与部署可使用独立的 CI Bearer 接口。JSON 请求使用 `Content-Type: application/json`。当前内嵌控制台提供这些流程的浏览器入口。

## 节点管理接口

| 请求 | 权限与输入 |
| --- | --- |
| `GET /api/v1/agents` | 平台管理员。返回远程 Agent 列表，有自定义名称时包含 `display_name`，主服务的本机显示名使用 `local_display_name` |
| `GET /api/v1/nodes/{agent}/resources` | 平台管理员。查看节点资源快照、系统主机名、可选的显示名及当前代次的连接状态 |
| `POST /api/v1/nodes/{agent}/display-name` | 平台管理员。同源 JSON 请求提供 `display_name`，最多 64 个字符，留空清除自定义名称。成功返回 204，名称改变时写入平台审计 |
| `GET /api/v1/container-webhooks`、`POST /api/v1/container-webhooks`、`DELETE /api/v1/container-webhooks/{id}` | 平台管理员，使用控制台会话。查看、创建和撤销独立容器更新绑定。创建时生成绑定专用的 Bearer token，提交和查询操作均使用该 token。[独立容器 Webhook](integrations.md#独立容器更新-webhook) |

## 流水线接入权限

平台管理员在「系统设置 → 流水线接入」维护容器目标、签发和撤销 CI 令牌、查看容器发布记录。签发时可以授权本机和远程的多个目标。每个授权由项目和目标共同确定；Compose 目标还可单独授予发布权限。令牌只在创建响应中显示一次，列表只显示范围、有效期和撤销状态。

| 请求 | 权限与输入 |
| --- | --- |
| `GET /api/v1/ci/targets`、`POST /api/v1/ci/targets` | 平台管理员。查询已配置目标，或创建绑定到指定节点代次、容器名及镜像仓库的独立容器目标 |
| `GET /api/v1/ci/targets/{target}/tokens`、`POST /api/v1/ci/targets/{target}/tokens` | 平台管理员。查看或签发令牌。签发请求包含 `id`、`ttl_hours`、`allow_publish`，以及可选的其他目标 `grants`，每项包含 `project`、`target` 和 `allow_publish` |
| `DELETE /api/v1/ci/targets/{target}/tokens/{id}` | 平台管理员。从任一已授权目标撤销整枚令牌，影响它的所有目标 |
| `GET /api/v1/ci/targets/{target}/runs` | 平台管理员。查看该独立容器目标的最近运行记录 |
| `POST /api/v1/container-deployments`、`GET /api/v1/container-deployments/{id}` | CI Bearer 接口。仅可向令牌允许的目标提交镜像摘要；查询仅返回由同一枚令牌提交的任务 |

## 项目接口

| 请求 | 权限与输入 |
| --- | --- |
| `POST /api/v1/projects/{project}/members/{username}` | 平台或项目管理员；`{"role":"operator"}`。`role` 支持三种角色及 `none`；`none` 移除项目访问 |
| `GET /api/v1/projects/{project}/members` | 平台或项目管理员；用返回的用户名游标 `next` 作为 `after` 继续分页 |
| `POST /api/v1/projects/{project}/deployments` | 操作员或管理员；`Idempotency-Key` 和 `{"target":"prod","release":"v1"}` |
| `GET /api/v1/projects/{project}/deployments/{id}` | 有权访问该项目的用户，查看部署快照 |
| `POST /api/v1/projects/{project}/deployments/{id}/cancel` | 操作员或管理员；`{"note":"取消原因"}` |
| `POST /api/v1/projects/{project}/deployments/{id}/rollback` | 操作员或管理员；控制台模板来源的部署由平台管理员回滚。使用历史成功部署 ID、`Idempotency-Key` 和 `{"note":"回滚原因"}` |
| `GET /api/v1/projects/{project}/audit` | 平台或项目管理员；可选整数游标 `after` |
| `GET /api/v1/audit` | 平台管理员；默认用 `after` 向后读取。使用 `before=0` 从最新事件开始倒序读取，再把返回的 `next` 作为 `before` 翻页 |

## 审计

平台管理员在「系统设置 → 平台审计」查询最新的平台事件，包括用户创建、密码重置以及独立容器 Webhook 的创建和撤销。列表按时间倒序分页。Webhook 的事件关联绑定 ID 和容器，审计内容不含 token。

审计在业务事务中写入，每页最多 50 条，使用 `next` 继续。项目审计覆盖部署请求与状态、取消、成员、CI 凭证、目标注册、密钥版本创建和撤销。多目标 CI 令牌的签发和撤销记录在对应目标所属项目的审计中。

用户操作记录稳定用户 ID，CLI 与系统操作分别标记。密钥事件记录版本引用，CI 凭证事件记录凭证 ID。平台管理员在「系统设置」维护镜像仓库和 Compose 模板，并负责节点 Docker 操作。节点操作有独立记录。
