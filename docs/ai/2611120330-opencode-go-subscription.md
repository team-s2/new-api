# OpenCode Go Subscription 渠道

新增渠道类型 `104`（`OpenCode Go Subscription`），把 OpenCode Go 订阅当作 OpenAI / Anthropic / Gemini 兼容上游接入 new-api；同时接入控制台用量弹窗与内置 QQ 机器人的 `/quota` 额度图。

> 分支 `feat/opencode-go`，基于含 Devin Subscription（`103`）的 `origin/main`。本文档为开发记录，是否保留由 owner 决定。

## 上游事实（已用真实订阅实测）

OpenCode Go 网关 `https://opencode.ai/inference/go`（旧地址 `https://opencode.ai/zen/go/v1` 会自动迁移）同时提供四套原生协议，同一文本模型可通过任一协议调用（官方文档称 Console 负责转换）：

| 协议 | 路径 | 实测 |
| --- | --- | --- |
| OpenAI Chat Completions | `/openai/v1/chat/completions` | 非流式、流式均 200 |
| OpenAI Responses | `/openai/v1/responses` | 200 |
| Anthropic Messages | `/anthropic/v1/messages` | 200 |
| Google generateContent | `/google/v1beta/models/{model}:generateContent`（流式 `:streamGenerateContent?alt=sse`） | 200 |

其他接口：

- 模型目录：`GET /v1/models`，`Authorization: Bearer <API Key>`。返回 `{"data":[{"id","type":"chat",...}]}`；带认证时只返回当前工作区已启用模型（本次测试账号仅有 `deepseek-v4.1-flash`，其他模型返回 `403 Model access is disabled`）。无认证请求会返回完整目录，但其中模型未必可用。
- 额度：`GET /v1/usage`，`Authorization: Bearer <API Key>`，返回：

```json
{"usage":{
  "rolling":{"status":"ok","percent":2,"resetsAt":"2026-11-11T22:37:48.000Z"},
  "weekly":{"status":"ok","percent":0,"resetsAt":"2026-11-16T00:00:00.000Z"},
  "monthly":{"status":"ok","percent":0,"resetsAt":"2026-12-11T17:23:43.000Z"}
}}
```

`rolling` 为 5 小时滚动窗口，`percent` 是已用百分比（0–100），`status` 为 `ok` / `rate-limited`。

请求头：`Authorization: Bearer` 对四种协议均可用；`x-opencode-session` 用于路由与 prompt cache，客户端提供时原样转发，否则按（渠道、用户、模型）派生稳定哈希；`User-Agent` 使用 `new-api/<version>`，符合官方“标识自身客户端”的要求。额度/目录探测禁止跟随重定向，避免把 Bearer 凭据转发到其他主机。

## 实现

### 后端

- `constant/channel.go`：`ChannelTypeOpenCodeGoSub = 104`，默认 BaseURL `https://opencode.ai/inference/go`，显示名 `OpenCode Go Subscription`，`NormalizeOpenCodeGoBaseURL` 兼容旧地址与误粘贴的 `/v1` 后缀。
- `constant/api_type.go` / `common/api_type.go`：`APITypeOpenCodeGoSub` 及渠道映射。
- `relay/channel/opencodego/`：
  - `adaptor.go` 按 `info.GetFinalRequestRelayFormat()` 选择上游路径，并把请求/响应转换委托给现有 `openai` / `claude` / `gemini` adaptor（原生透传，不做协议转换）。`DoResponse` 同理分派，所以计费仍走既有 token 结算。
  - `constant.go` 保存路径、渠道名与公开目录兜底列表。
  - `relay/relay_adaptor.go` 注册；`relay/common/relay_info.go` 加入 `streamSupportedChannels`，保留 `stream_options.include_usage`。
- `service/opencodego_usage.go`：
  - `FetchOpenCodeGoUsage` 校验三个窗口、百分比范围、状态与 RFC3339 重置时间，并统一为 UTC。
  - `FetchOpenCodeGoChannelModels` 只保留 `type=chat`、未废弃、去重排序的模型。
  - 错误信息只保留短原因、脱敏 API Key、限制 1 MiB 响应并禁止重定向。
- `controller/opencodego_usage.go` + `router/channel-router.go`：`GET /api/channel/:id/opencode-go/usage`，`authz.ChannelRead`，`Cache-Control: no-store`；单 key 校验，业务失败以 HTTP 200 + `success:false` 返回，并提供 `message_key` 供控制台翻译。多 key 渠道的额度查询被拒绝（中继本身仍支持多 key）。
- `controller/channel_upstream_update.go`：模型发现走认证目录接口，并经 `sanitizeFetchModelsError` 脱敏。

本渠道 Key 就是 OpenCode Console 签发的 `oc_sk_...` API Key，无 OAuth、无刷新任务、无数据库改动。

### 前端（`web/src/features/channels/`）

- `constants.ts`：类型 104、名称、供应商说明、排序、`MODEL_FETCHABLE_TYPES`、Key 提示。
- `api.ts`：`getOpenCodeGoUsage` 与响应类型。
- `components/dialogs/opencode-go-usage-dialog.tsx`：5 小时 / 每周 / 每月三张卡片，显示已用百分比、进度条、重置时间与限流状态，支持刷新与 Raw JSON。
- `channels-columns.tsx`：余额列点击打开用量弹窗，文案 `Click to view OpenCode Go subscription usage`。
- `lib/channel-utils.ts`：图标映射。
- 7 个 locale + `static-keys.ts`：新增 UI 与后端 `message_key` 的翻译。

### 内置 QQ 机器人

- `qqbot/client.go` 挂载 `GET /channel/:id/opencode-go/usage`。
- `qqbot/quota.go`：新增 `kindOpenCodeGo`，把 `rolling`/`weekly`/`monthly` 映射到 5h/周/月三个窗口；任一窗口 `rate-limited` 时显示“限流”；`/quota` 与 `/quota all` 自动包含该渠道。
- `qqbot/render/quota.go`：月窗口存在时占用“重置卡”列（与 Grok 相同），其余渠道行为不变。

## 验证

真实订阅（本机 DSH 凭据，未写入仓库或日志）：

- `FetchOpenCodeGoUsage`：HTTP 200，三窗口正常解析并归一化为 UTC。
- `FetchOpenCodeGoChannelModels`：返回该账号实际可用的 `deepseek-v4.1-flash`；其他模型在此账号为 `403`，印证应使用认证目录。
- 通过真实 adaptor 的 `Convert → DoRequest → DoResponse` 串联测试：OpenAI 非流式、OpenAI 流式、Claude Messages、Responses、Gemini 全部 HTTP 200，响应体格式正确。

本地检查：

- `GOTOOLCHAIN=auto go build ./...`、`go vet ./relay/channel/opencodego ./service ./controller ./qqbot/... ./middleware ./router ./constant ./common`：通过。
- `go test ./relay/... ./service ./controller ./middleware ./router ./constant ./common ./qqbot/...`：通过。
- `cd web && ./node_modules/.bin/tsgo -b`：通过；改动文件 `oxlint` 0 error、`oxfmt --check` 通过；`bun run build` 成功；`bun run i18n:sync` 各语言 0 missing / 0 extras。
- `vitest` 在当前 Node 22.11 环境因既有依赖 `html-encoding-sniffer` 的 `ERR_REQUIRE_ESM` 无法启动（与本次改动无关）；未新增永久单元测试，临时测试已删除。
- 无数据库结构、迁移或共享行锁改动，因此未执行三数据库矩阵验证。

## 风险与待观察

- 模型可用性由工作区隐私与模型访问设置决定；目录只展示当前 Key 可用模型。若账号启用更多模型，重新“获取模型列表”即可。
- 会话头回退值是“用户 + 渠道 + 模型”粒度，不等同 DSH 插件的逐会话 ID，prompt cache 复用率可能略低；客户端若自带 `x-opencode-session` 会原样转发。
- 免费模型（如 `beam-free`）如出现在 Go 目录中可按普通模型使用；单独的匿名 Free 通道（`Bearer public` + 官方 CLI UA）未实现，也不在本次范围内。
- 官方额度窗口的时间格式与字段名可能变化；解析失败会返回明确错误而不是零值。
- 计费仍按 new-api 的模型倍率/价格结算；订阅额度仅用于展示，不做准入或优先路由。
