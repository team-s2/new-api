# 智谱 Coding Plan OAuth 渠道

本 fork 的 `BigModel Subscription (Coding Plan)` 渠道使用类型 **100**，通过 OAuth 登录取得包含 `api_key` 和 `access_token` 的 JSON 凭据。`api_key` 用于模型调用，`access_token` 用于订阅用量查询。普通智谱 V4 渠道（类型 26）配合 `glm-coding-plan` 地址仍是独立的接入方式。

支持“从上游获取”模型列表和上游模型更新检查。

## ultra 端点（1.5x 额度）

类型 100 的默认 `base_url` 是预设 `zcode-ultra`：**Anthropic Messages 协议**的请求发往 ZCode 的 ultra 路由，OpenAI 协议的对话/向量/图像、Responses 与模型列表仍走直连 coding 端点（ultra 路由只提供 `/v1/messages`）。

| 用途 | 国内（BigModel） | 国际（Z.AI） |
| --- | --- | --- |
| Claude 协议（ultra） | `https://zcode.z.ai/api/v1/ultra/anthropic` | `https://zcode.z.ai/api/v1/ultra-zai/anthropic` |
| OpenAI 协议 / 模型列表 | `https://open.bigmodel.cn/api/coding/paas/v4` | `https://api.z.ai/api/coding/paas/v4` |
| Responses | `https://open.bigmodel.cn/api/v1` | — |

国际站预设名为 `zcode-ultra-international`。

ultra 路由由上游按 **1.5 倍额度** 计量（扣减约为直连 coding 端点的 0.667 倍）。出站鉴权沿用渠道凭据里的 `api_key`（CodingPlan 项目 key），统一 `Authorization: Bearer`，不追加额外请求头，也不校验客户端指纹。

`base_url` 留空按渠道类型默认解析到 `zcode-ultra`；显式填 `glm-coding-plan` 仍走改造前的直连行为，旧渠道可据此回退。

## 用量查询

`GET /api/channel/:id/zhipu/coding-plan/usage` 透传 `open.bigmodel.cn` 的
`/api/monitor/usage/quota/limit` 结果。智谱自 2026-07-30 起将个人套餐改为按积分计费
（见官方文档「老用户权益说明」），新旧两代套餐的限额条目格式并存：

- 旧版套餐按 prompts 计数：5 小时与每周窗口为 `TOKENS_LIMIT`，另有 `TIME_LIMIT`
  的 MCP 每月窗口。
- 新版积分套餐（如 lite）的窗口为 `CREDIT_LIMIT`，同样只有 5 小时与每周两个窗口；
  MCP 调用直接从积分池扣除，不再有独立的每月 MCP 窗口。

两代套餐共用窗口编码（unit 3 × number 5 = 滚动 5 小时，unit 6 × number 1 = 7 天周）。
后端把两种 type 都映射到 `five_hour` / `weekly`，并在每个窗口附带 `unit` 字段
（`credits` 或 `prompts`；MCP 每月窗口按次计数，无此字段）供前端显示单位。
套餐不含某窗口时对应字段缺省，前端显示「当前套餐不包含此额度」。

## 重置卡

OAuth 凭据中同时保存 `zcode_jwt`，用于查询 zcode.z.ai 的 Coding Plan 重置卡
（5 小时 / 每周）。`/api/channel/:id/zhipu/coding-plan/usage` 的响应在
`reset` 字段中附带可用重置卡数量与最近使用记录；渠道编辑界面的用量对话框
提供“执行重置”按钮消耗一张卡。也可以单独调用：

- `GET /api/channel/:id/zhipu/coding-plan/reset-status`
- `POST /api/channel/:id/zhipu/coding-plan/reset`（body: `{"reset_type": "FIVE_HOUR" | "WEEK"}`）

在重置卡功能加入前保存的旧凭据没有 `zcode_jwt`，重置卡信息会静默缺失；
重新执行一次 OAuth 登录即可补全。默认通过 `GET https://open.bigmodel.cn/api/coding/paas/v4/models` 获取，使用 OAuth 凭据中的 `api_key` 进行 Bearer 认证；无需手动填写协议端点或将整段凭据作为请求头发送。

## 类型编号兼容性

类型 **61** 属于上游 Task Plugin，不能重用。前后端新增 Coding Plan 功能时应分别使用 `ChannelTypeBigModelSub` 和 `CHANNEL_TYPE_BIGMODEL_SUB`，避免显示名称、OAuth 控件、凭据校验、订阅用量和实际中继使用不同编号。

早期 fork 曾把 Coding Plan 写入类型 61。后续只修正了类型名称和后端编号，部分前端逻辑仍检查 61，导致原渠道显示为 Task Plugin，而真正的 Coding Plan 缺少 OAuth 和用量控件。当前版本已统一。

升级旧 fork 时：

1. 类型已为 100 的 Coding Plan 渠道无需迁移。
2. 上游 v1.0.0-rc.40 起将 62/63 分配给 vLLM/SGLang 渠道。为避免再次迁移，本 fork 的 Coding Plan 编号固定在 100（64-99 预留给上游）。升级后在编辑界面确认渠道显示为 Coding Plan；如显示异常，选择 Coding Plan 并保存即可，不需要更换凭据时将密钥输入留空。
3. 确认旧类型 61 渠道实际属于 Coding Plan 后，在编辑界面选择 Coding Plan 并保存；不需要更换凭据时将密钥输入留空。更新请求会省略密钥，后端保留已有 JSON。
4. 不要将所有类型 61 的数据库记录批量修改为 100；真正的 Task Plugin 必须保留原类型。程序不通过猜测凭据内容自动转换渠道。

审计日志中的 `changed_fields` 能确认一次编辑是否更换了密钥，但不保存旧密钥内容。已被覆盖或删除的凭据不能仅凭审计日志还原，需要有效备份或重新 OAuth 登录。

### 自动领取

主节点启动后自动轮询 OAuth Coding Plan 渠道的 `/reset/opportunity`，无需打开用量页面。启用中的渠道及因高峰期自动停用的渠道参与领取；手动停用、多 Key 和缺少 `zcode_jwt` 的渠道跳过。沿用渠道代理，不自动使用重置卡。

领取成功后立即查询卡片状态，并通过 QQ 机器人向符合渠道分组过滤条件的绑定群发送通知；机器人未连接时仅记录日志，不补发。调度与官方 ZCode 客户端一致：成功或限流（HTTP 429 或业务码 429）后等待 10 分钟；暂不可领（业务码 `3301`）时取 `next_try_at` 与当前时间加 5 分钟的较晚值，缺少有效的 `next_try_at` 按错误处理。网络异常、超时或业务码 `2007` 保留幂等键，5 分钟后重试；收到其他任何响应（包括 5xx 和无法解析的响应）都清空幂等键，等待 10 分钟。上游在 HTTP 4xx 时仍会在信封中返回业务码，因此先读业务码再看 HTTP 状态。每 5 分钟检查到期任务，顺序处理；同一 ZCode JWT 共用领取状态。冷却及幂等键仅存于进程内，重启后重新开始；部署应只配置一个 master。
