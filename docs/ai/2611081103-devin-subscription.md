# Devin Subscription 渠道调研

调研结论先行：Devin Desktop/CLI 的模型流量走 Cognition（收购 Windsurf 后）的 Codeium 后端 `server.codeium.com`，协议为 Connect-RPC（protobuf 服务端流）。该面已被两个 MIT 开源项目完整逆向，接入可行；实现成本集中在 protowire 手写编解码层（有现成参照），其余可完全复用本仓库 Antigravity Subscription 已建立的渠道模板。本文档记录逆向事实、接入设计与风险，供实现 PR 参考。

- 目标：新增 `Devin Subscription` 渠道类型，渠道 Key 为 Devin OAuth（PKCE）登录所得 session token（可叠加 `cog_*` durable key 做自愈），模型面仅暴露 `swe-2` 与 `swe-2-flash` 系列。
- 场景定位：Pro（自助付费）账号自用；`swe-2`/`swe-2-flash` 当前对自助档处于促销免费窗口（见「免费口径」）。

## 参考实现

| 项目 | 协议路线 | 参考价值 |
| --- | --- | --- |
| `WncFht/devin2api` | 从 CLI 二进制抽取 proto bundle，vendored 生成绑定（`outputs/devin-proto-go`，约 10MB）+ `connect-go` 客户端 | 逆向文档最详尽：`docs/upstream-protocol.md`、`docs/upstream-rate-limit.md`、`docs/quota-billing.md`、`docs/devin-accounts.md`；另有 `outputs/devin-proto/all-protos.proto` 扁平 proto 与 wire corpus |
| `router-for-me/CLIProxyAPI` | **手写 `protowire` 编解码，无生成绑定**（`internal/runtime/executor/helps/devin_wire.go`） | 最贴合本仓库：`google.golang.org/protobuf` 已是间接依赖，`encoding/protowire` 转直接依赖即可，零新增第三方库；登录流见 `internal/auth/devin/devin_auth.go` |

两者均为 MIT，移植代码需保留署名。

## 上游协议事实

### 传输与端点

- Base URL：`https://server.codeium.com`（env `WINDSURF_API_SERVER_URL` 可覆盖，CLI 二进制 strings 可证）。
- 聊天：`POST /exa.api_server_pb.ApiServerService/GetChatMessage`，`application/connect+proto` + `connect-protocol-version: 1`，Connect 服务端流，帧格式 `[flag:1B][len:4B BE][payload]`（flag 0x00 数据 / 0x01 gzip / 0x02 end-stream）。
- 配额：`POST /exa.seat_management_pb.SeatManagementService/GetUserStatus`（unary，`application/proto`，原始 protobuf body）。
- 模型目录：`POST /exa.api_server_pb.ApiServerService/GetCliModelConfigs`（unary）。
- 铸 token：`POST /exa.seat_management_pb.SeatManagementService/GetSelfDevinSessionToken`（unary；用 `cog_*` durable key 铸新 session token）。
- 通用头部：`Sentry-Trace: <32hex>-<16hex>-1`（仅流式 RPC 携带，unary 不带）；CLI 原生不发送 `User-Agent`。

### 认证

Devin CLI 登录是公开客户端 PKCE 流（无 client_id/client_secret）：

1. 授权页：`https://app.devin.ai/auth/cli/continue?state=…&prompt=select_account&code_challenge=…&code_challenge_method=S256`；无 `redirect_uri` 时加 `cli_pkce_marker=1`，授权完成后浏览器直接展示 code 文本（手动粘贴模式，适合服务器端部署，语义同 Antigravity 的粘贴回调 URL）。
2. 换取 token：`POST https://api.devin.ai/auth/cli/token`，JSON `{code, code_verifier}` → `{token}`（JWT 形态，前缀补 `devin-session-token$`）。
3. 账户信息：`GET https://api.devin.ai/v3/self`，`Bearer <token>` → `{user_name, user_id, org_id}`。

线上形态（两处同时携带）：`Authorization: Basic <session_token>-<session_token>`；`metadata.api_key = <session_token>`。session token 寿命由服务端管理，无 refresh_token；`cog_*` durable key（app.devin.ai → Settings → API keys 手工签发，撤销才失效）可经 `GetSelfDevinSessionToken` 现场铸新，是零维护兜底。

### GetChatMessageRequest 关键字段（protowire 编码序号）

| 字段 | 内容 |
| --- | --- |
| 1 `metadata` | 子消息：1=`chisel`、2=`3000.x.y` 版本、3=api_key(session token)、4=`en`、5=os、7=版本、12=`chisel`、31=732-hex 设备指纹 `f`（CLI 每请求随机生成，上游接受缺失但建议对齐） |
| 2 `prompt` | system prompt 文本 |
| 3 `chat_message_prompts` | 历史消息：1=message_id(UUID)、2=source(1=user/2=assistant/4=tool)、3=prompt、6=tool_calls{1:id,2:name,3:args_json}、7=tool_call_id、10=images{1:b64,2:mime}、11=thinking、12=signature、18=signature_type |
| 7 `request_type` | 必须为 `CASCADE(5)`——唯一可无状态使用的类型，其余需 StartCascade 轨迹 |
| 8 `configuration` | 1=num_completions、2=max_tokens、3=max_newlines、5=temperature(f64)、7=top_k(40)、8=top_p(f64,≈0.95) |
| 10 `tools` | 1=name、2=description、3=json_schema_string |
| 12 `tool_choice` | oneof：option_name 合法值 `none`/`auto`/`required`（`any` → `invalid_argument`）、tool_name 强制调用 |
| 15 `trajectory_reference` | 1=session_id、2=step_index（会话内单调计数）、3=4、4=14（user-turn 边界） |
| 16 `cascade_id` | 会话稳定的 prompt cache 键 |
| 20 `planner_mode` | 1 |
| 21 `chat_model_uid` | 必填；版本化 uid（如 `swe-2-high-09102026`）→ `permission_denied` |
| 27 `prompt_cache_key` | prompt cache 复用键 |

### GetChatMessageResponse 帧字段

| 字段 | 内容 |
| --- | --- |
| 3 `delta_text` | 文本增量 |
| 5 `stop_reason` | 10=FUNCTION_CALL、2/3 等为常规终止 |
| 6 `delta_tool_calls` | 子消息 {1:id,2:name,3:arguments_json,4:invalid_json_str,5:invalid_json_err,6:is_custom} |
| 7 `usage` | **权威用量**：input/output/cache_read/cache_write tokens + model_uid + billing_model_uid + response_header(x-request-id) |
| 9/10/21 | `delta_thinking` / `delta_signature` / `signature_type`——思考签名可回放进下一轮历史（Claude `redacted_thinking`、Responses `encrypted_content` 等价物） |
| 15/16/17/23 | `output_id` / `thinking_id` / `request_id` / `actual_model_uid` |
| 28 `response_dimension_groups` | 备用用量来源（usage 缺省时解析） |

错误语义（实测）：`resource_exhausted` = 限流（带 reset hint）；`failed_precondition` = 日/周配额耗尽或历史形态违规；`unauthenticated` = token 失效；`invalid_argument`/`unknown` = 请求形态问题（部分会 mask 成第三方瞬时错误文案，按 code 判）；`unimplemented` = 模型容量硬币翻转，非限流。

## 模型目录与"免费"口径

- `GetCliModelConfigs` 动态下发 `client_model_configs[]`（约 247 条，含 `label`、`model_uid`、`allowed_tiers`、`pricing_type`、`model_cost_tier`、`promo_status`、`is_capacity_limited`、能力位），目录按 seat/套餐定制——**不允许的模型由上游直接拒**。
- `swe-2`：thinking levels medium/high/max，支持图片，ctx 262K；`swe-2-flash`：medium/high/max，纯文本，ctx 262144。uid 规则 `<family>-<effort>`；CLIProxyAPI 约定 `swe-2*` 默认 effort 为 `high`（`swe-2`→`swe-2-high`、`swe-2-flash`→`swe-2-flash-high`）。
- 促销窗口：SWE-2 对自助档免费至 **2026-11-30**；SWE-2 Flash 免费至 **2026-12-04**（之后 $0.10/$0.50 per Mtok）。Free plan 仅 Flash Medium；Pro 全档可用。促销结束后这些模型转为烧 seat 配额（日 ≈$14、周 ≈$25 目录价）。
- 渠道设计按模型家族限制：仅暴露 SWE-2 与 SWE-2 Flash 及已支持的 effort 变体。免费促销不是模型准入条件；促销结束不会自动下架这些模型。支持名单与裸名映射统一定义在 [`common/devin_models.go`](../../common/devin_models.go)。

## 配额与限流

- `GetUserStatus` → `plan_info`：`daily_quota_remaining_percent`、`weekly_quota_remaining_percent`、`daily/weekly_quota_reset_at_unix`、`overage_balance_micros`（百分比为 int32 向下取整；归零后字段返回 null）。
- 计费口径（devin2api 拟合）：`(input+cache_write)·p_in + cache_read·p_cached + output·p_out`，cache_write ≈ 1.0× input 价；入账延迟 1–3 分钟；无价格维的 free 档模型燃烧低于检出限。
- 限流两类：账号级分钟桶（`Reached overall message rate limit ... reset in N`，含不可见流量，重试尝试计入）；2026-09 起 free-model 滑窗惩罚器（被拒尝试计入 ~2h 预算，惩罚时长几何爬坡）。**实现侧必须在渠道内节制重试，429 不重试同渠道**——new-api 天然换渠道重试，配合多 key 池化摊薄。

## 接入设计（对照 Antigravity Subscription 模板）

新增 `ChannelTypeDevinSub`（下一个可用编号）+ `APITypeDevinSub`，默认 base `https://server.codeium.com`。

### 凭据

`channel.Key` 存 JSON：

```json
{
  "session_token": "devin-session-token$…",
  "api_key": "cog_…(可选，铸新兜底)",
  "user_name": "…", "user_id": "…", "org_id": "org-…",
  "plan": "pro", "created_at": "…"
}
```

刷新语义与 antigravity 不同：无 refresh_token。请求侧仅在 session token 缺失且存在 `api_key` 时惰性铸新；手动刷新会重新铸造已有 token。网络调用在行锁外执行，持久化前核对凭据和上游配置，避免覆盖并发修改。只有 OAuth session token 的渠道需重新登录。当前请求优先使用 `api_key`，不宣称自动检测 session token 到期。具体流程见 [`service/devin_credential.go`](../../service/devin_credential.go)。

### 文件清单

- `relay/channel/devinsub/adaptor.go`：内嵌 `openai.Adaptor`（OpenAI Chat/Responses 与 Claude Messages 经宿主桥接）；`GetRequestURL` → `{base}/exa.api_server_pb.ApiServerService/GetChatMessage`；`SetupRequestHeader` 设 Basic 认证、connect+proto、Sentry-Trace、suppress UA；`Convert*` 做消息映射 + 别名解析 + protowire 编码；`DoRequest` 发送后用流转换 reader 把 Connect 帧翻译成 OpenAI chunk SSE（同 `antigravitySSEUnwrapReader` 手法），`DoResponse` 完全复用 openai。
- `relay/channel/devinsub/wire.go`：protowire 编解码 + Connect 分帧（移植 `devin_wire.go` 子集：metadata/历史/工具/tool_choice/configuration/trajectory 编码，帧解析 delta/tool_call/usage/thinking；签名回放与 dimension_groups 用量回退尚未实现）。
- `relay/channel/devinsub/constant.go`：base URL、RPC 路径、客户端指纹（`chisel`/`3000.x.y`）、模型别名表（`swe-2`/`swe-2-flash` → 默认 effort）。
- `service/devin_oauth.go`：PKCE start/exchange + state/owner-session 存储（复用 antigravity session 骨架）+ `/v3/self` profile 回填。
- `service/devin_credential.go`：credential 解析/合并、`GetSelfDevinSessionToken` 铸新、lazy ensure。
- `service/devin_usage.go`：`GetUserStatus` 账户信息 + `GetCliModelConfigs` 目录过滤；未验证的配额与能力字段不对外展示。
- `controller/devin_oauth.go`：`POST /devin/oauth/login`、`POST /devin/oauth/exchange`、`POST /:id/devin/refresh`、`GET /:id/devin/usage`（权限点同 antigravity）。
- 接入点：`constant/api_type.go`、`constant/channel.go`、`relay/relay_adaptor.go`、`relay/common/relay_info.go`（streamSupportedChannels）、`middleware/distributor.go`（lazy ensure）、`service/channel_select.go`（如需要）、`router/channel-router.go`、`controller/channel.go`（Key JSON 校验 + FetchModels 豁免拆分）、`controller/channel_upstream_update.go`（模型发现）。
- `web/`：`devin-oauth-login-dialog.tsx`（开登录页 + 粘贴 code）、usage dialog、drawer 集成、`api.ts`、`constants.ts`、7 个 locale 文件。

### 模型暴露

- 支持名单与请求校验共用 [`common/devin_models.go`](../../common/devin_models.go)，只允许 SWE-2 与 SWE-2 Flash 家族中的明确条目。
- 模型发现仅保留名单内且未禁用的目录 UID；对应 `high` UID 存在时才添加裸名别名。账户实际调用权限仍由上游校验；不根据未验证的套餐或促销字段保证模型免费或可用。

### 待决细节

- **system prompt 消毒**：上游服务端注入约 390 token 的 Codeium 身份 system prompt；CLIProxyAPI 另有客户端侧消毒（剥 Claude Code 归属行、敏感词混淆）防指纹。建议默认原样转发，渠道 setting 加开关。
- **会话亲和**：`trajectory_reference`/`cascade_id`/`prompt_cache_key` 决定 prompt cache 命中；参照 antigravity `deriveAntigravitySessionID` 按 (channel,user,model) 稳定派生，配合 new-api 既有渠道亲和。
- **计费**：usage 帧给出真实 tokens（含 cache 维度），映射 `Usage`；渠道侧价格由管理员按 ratio 配置，促销期建议置 0。
- **用量探测中的目录缓存**：`GetCliModelConfigs` 体积不小，建议按渠道缓存 + usage dialog 强制刷新。

## 风险

- 非官方逆向面，协议可随时变化（devin2api 的 protocol-drift 记录显示 2026-09 月内多次细节变化）；ToS 风险与 Antigravity/Grok 同级，限自用账号。
- 免费窗口到期后模型烧 seat 配额；日/周任一归零付费模型被 `failed_precondition` 拦。
- 429 惩罚器对突发敏感：需在渠道限速策略与失败重试上保守；多 key（多账号）池化是主要摊薄手段。
- session token 无公开寿命承诺，批量部署建议必配 `cog_*` 兜底。

## 验证计划

1. `devin auth login`（本机 CLI）拿 `credentials.toml` 的 `windsurf_api_key` 或走 PKCE 粘贴流拿 session token。
2. 探 `GetCliModelConfigs`：确认 swe-2/swe-2-flash 各 effort uid、`allowed_tiers`、`promo_status`。
3. 探 `GetUserStatus`：确认 plan、日/周配额字段。
4. 最小 `GetChatMessage`（单条 user prompt、`request_type=CASCADE`、无 tools）流式回归通 → 再补 tools/thinking/图片。
5. 限流观察：低 rpm 起步，记录 `resource_exhausted` hint 与封号形态。

---

## 实现记录（feat/devin-subscription，基于 fork main）

落地结构与本调研的方案一致：手写 `protowire` wire 层（参照 CLIProxyAPI `devin_wire.go`），无生成 proto 依赖；凭据/认证在 `service/`，传输在 `relay/channel/devinsub/`。

**后端**

- `constant/channel.go`：`ChannelTypeDevinSub = 103`，默认 BaseURL `https://server.codeium.com`；`constant/api_type.go` / `common/api_type.go`：`APITypeDevinSub`；`relay/common/relay_info.go`：加入 `streamSupportedChannels`；`relay/relay_adaptor.go`：注册 `devinsub.Adaptor`。
- `common/devin_models.go`：统一模型名单与裸名映射，供 relay 与模型发现复用。
- `relay/channel/devinsub/wire.go`：Connect 帧编解码、chisel 元数据（732-hex 指纹）、`GetChatMessage` 请求构造（CASCADE、prompts/tools/tool_choice/trajectory_reference）、响应帧解析（text/thinking/tool_calls/usage/stop_reason）、trailer 错误解析、UTF-8 边界缓冲。
- `relay/channel/devinsub/adaptor.go`：`DoRequest` 发 connect+proto 后用 `io.Pipe` reader 把帧流翻译成 OpenAI SSE（role/content/reasoning/tool_call/usage/finish/[DONE]），非流式聚合为 `chat.completion`；`DoResponse` 复用 OpenAI 处理器；会话亲和按 `prompt_cache_key` 否则 `(channel,model,user)` 派生 SHA1 UUID；UA 置空，`Sentry-Trace` 仅 chat 请求。
- `service/devin_credential.go`：凭据 JSON（`session_token`/`api_key`/`user_id`/`org_id` 等）；`AccessToken()` 优先 `cog_*` api_key；`EnsureDevinChannelSessionToken` 仅在 session_token 缺失且存在 api_key 时经 `GetSelfDevinSessionToken` 铸新，行锁持久化。
- `service/devin_oauth.go`：PKCE（S256）登录，`app.devin.ai/auth/cli/continue` + `api.devin.ai/auth/cli/token`，state/session 绑 owner、10min TTL，接受粘贴 code 或回调 URL。
- `service/devin_wire.go` / `service/devin_usage.go`：`GetUserStatus` + `GetCliModelConfigs` 探测，输出账户信息与受支持模型目录；`FetchDevinChannelModels` 按目录过滤 swe-2 家族 uid（供渠道上游模型同步），含裸名别名。
- `controller/devin_oauth.go` + `router/channel-router.go`：`POST /devin/oauth/login`、`POST /devin/oauth/exchange`、`POST /:id/devin/refresh`、`GET /:id/devin/usage`。
- `middleware/distributor.go`：非 multi-key 渠道惰性 `EnsureDevinChannelSessionToken`（每次请求仅一次 JSON 解析）。
- `controller/channel.go`：key 必须是合法凭据 JSON；`controller/channel_upstream_update.go`：Devin 模型同步走目录探测。
- 无周期刷新任务：session token 寿命服务端可控且不可预知，`api_key` 本身可作 bearer，惰性铸新已覆盖；如需预热可后续再加。

**前端**（`web/src/features/channels/`）

- `constants.ts`：`CHANNEL_TYPE_DEVIN_SUB = 103` + 名称/描述/排序；`api.ts`：OAuth login/exchange、`refreshDevinCredential`、`getDevinUsage`。
- `dialogs/devin-oauth-login-dialog.tsx`：两步 OAuth（打开授权页 → 粘贴 code/回调 URL → 回填 key）+ `DevinOAuthControls`（含个人使用免责声明）。
- `dialogs/devin-usage-dialog.tsx`：plan/账户徽章、SWE-2 模型目录卡、Raw JSON；不展示未验证的 credits/can_use 字段。
- `channel-mutate-drawer.tsx`：OAuth 控件 + 编辑态手动刷新按钮；`channels-columns.tsx`：余额列点击弹 usage（`isAccountInfoChannel` 纳入 Devin）。
- i18n：7 个 locale 文件各 +22 key，`static-keys.ts` 登记渠道名/描述。

**验证**

- `go build ./...`：通过（含 `web/dist` embed）。
- `bun run typecheck`：1,419 文件无错误；`bunx oxlint` 改动文件 0 error；`bunx oxfmt` 已格式化；`bun run build`（rsbuild）成功。
- `bun run i18n:sync`：7 locale 0 missing。
- 未做：真实上游端到端联调（需有效凭据）；Devin 会话令牌失效形态、限流惩罚行为需在灰度中观察。

**遗留/注意**

- 上游在服务端注入 ~390 token Codeium 身份 system prompt；当前原样转发客户端 system prompt，未加消毒开关。
- 429/惩罚器敏感：失败重试依赖 new-api 换渠道语义，同渠道不内置重试。
- 模型准入按 SWE-2 / SWE-2 Flash 家族的明确名单执行，促销标记不控制准入；网关计费仍由管理员配置。

**真机联调（2026-11-08，Pro 账号 devin-session-token）**

- `GetUserStatus`/`GetCliModelConfigs`/`GetSelfDevinSessionToken` 为 **unary** RPC：必须用 `application/proto` + 原始 protobuf body；`connect+proto` 信封返回 415（已修正 `postDevinConnectRPC`）。
- `GetChatMessage` 为服务端流：`connect+proto` 信封，流式/非流式端到端通过——reasoning、content、finish_reason、真实 usage（含 `cached_tokens`，观察到 prompt cache 命中 448/471）均正确。
- 目录实测（260 条）中 swe-2 家族共 9 个 uid：`swe-2-{medium,high,max}` + `swe-2-{medium,high,max}-lightning`（cost_tier 3，更低）+ `swe-2-flash-{medium,high,max}`；`-low` 与 `-usa` 变体未在目录中（保留在白名单内，上游拒绝时走 trailer 错误透出）。`-lightning` 变体已加入白名单并实测 `swe-2-max-lightning` 正常返回。
- 当时通过 service 侧镜像名单对齐模型过滤；这只能保证协议支持的 UID 一致，不能保证账户权限或上游可用性。
- `GetUserStatus` 的 credits/can_use_* 字段号系猜测，实测值不可信（cli=false 但账号实际可用）；这些历史观测保留供后续抓包校正，当前接口和界面已移除这些未验证字段。
- 未验证：`cog_*` 铸新路径（本机凭据只有 session token）；OAuth 全链路（authorize 页需人工登录）；多模态/tools。


## 审查修正（2026-11-08）

按用户确认的设计，准入范围保持为 SWE-2 和 SWE-2 Flash 家族；白名单、裸名解析和模型发现共用 `common/devin_models.go`。请求转换后、发送上游前再次检查模型，参数覆盖也不能绕过此限制。模型发现保留完整 JSON 凭据，前端开启对应入口。

Connect 流必须以合法 end-stream trailer 结束。首帧错误映射 HTTP 状态；后续 trailer 错误、截断和损坏帧产生错误事件。usage 与 finish 延迟到 trailer 校验后发送；失败时仅把已有 usage 交给宿主结算，再发送错误。OpenAI / Claude / Responses 桥接均不把该失败转换成成功结束；已有输出沿用宿主部分输出结算，无输出且无权威 usage 时走失败退款。

工具选择支持 `none`、`auto`、`required` 和已声明的命名函数。字段布局依据 [devin2api 提取的 protobuf](https://github.com/WncFht/devin2api/blob/main/outputs/devin-proto/all-protos.proto) 中 `ExaChatPb_ChatToolChoice`；Connect 终止语义依据 [Connect Protocol Reference](https://connectrpc.com/docs/protocol/)。图片 URL 和 data URI 共用现有文件服务，保留下载限制和 SSRF 校验，读取失败不再静默丢图。

手动刷新会铸造新 session token；并发更新了凭据、base URL 或 proxy 时拒绝覆盖，其他请求已经刷新的 token 则保留。数据库结构、SQL 与共享行锁实现没有修改。安全参考为 [OWASP ASVS 5.0.0](https://github.com/OWASP/ASVS/tree/v5.0.0)、[Authentication Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html) 和 [Session Management Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)；刷新错误日志不含上游凭据或响应内容。此处不声明完整 ASVS 合规。

本次本地验证使用临时 fixture，完成后删除，不新增永久单元测试：

- `go test -race ./relay/channel/devinsub -run TestReview -count=1`：通过。覆盖帧错误、截断、合法终止、工具选择、URL/data URI 图片、HTTP 首帧错误与最终模型准入。
- `go test -race ./relay/channel/devinsub -run TestReviewDevinBridgeTerminal -count=1`：通过。模拟 Connect 内容、usage/finish 与成功或失败 trailer，经实际 OpenAI / Claude / Responses handler 验证错误透传、无提前成功终止与 usage 保留。
- `go test ./service -run '^TestTemporaryDevinRefresh$' -count=1 -v`：分别使用 SQLite 3.50.4、MySQL 8.4.13、PostgreSQL 16.15 通过同一刷新 fixture；MySQL 使用 `TEST_MYSQL_DSN`，PostgreSQL 使用 `TEST_POSTGRES_DSN` 指向隔离临时数据库。覆盖强制刷新、惰性行为、并发 token 保留、配置变化拒写、上游失败及取消请求。SQLite 另以 `go test -race ./service -run '^TestTemporaryDevinRefresh$' -count=1` 通过。
- `go test ./controller -run TestTemporaryDevinDiscoveryContract -count=1`：通过。覆盖多行 JSON、模型家族过滤和裸名别名可用性。

- `go test ./relay/channel/devinsub ./relay/channel/openai ./relay/helper ./service ./controller ./middleware` 与 `go build ./...`：通过。
- `cd web && bun run typecheck`、改动文件 oxlint/格式检查、`bun run build`：通过；临时前端 fixture 验证模型发现入口开启且旧响应中的未验证额度字段不渲染。
- 文档范围检查：全仓库 Markdown（包含隐藏与忽略目录，排除 `.git`、依赖、构建及技能目录）搜索 `Devin` / `swe-2`，仅命中本文；删除了实现行数等派生描述，修正了免费过滤、锁内 mint 与额度展示说明，保留真实账号的历史观测。

交互式 OAuth 和 `cog_*` mint 尚未真实验证；图片与工具调用的真实账号结果见下节。配额字段未完成校正，因此暂不展示额度，而不是把猜测值作为可靠数据。


## 修正后的真实账号验证（2026-11-08）

使用本机 Devin CLI 已登录 Pro 账号的 session token。临时 Go harness 直接调用修改后的 adaptor（Convert → DoRequest → DoResponse）与真实 `server.codeium.com`，没有启动公开测试服务，也没有修改生产渠道或向工具发送真实业务数据。凭据从本机文件读取，不写入命令、日志、仓库或 PR；测试后删除 harness。

执行命令为 `DEVIN_LIVE_TEST=1 DEVIN_LIVE_CASE=<case> go test ./relay/channel/devinsub -run '^TestLiveDevin$' -count=1 -v`，按下表顺序串行执行，失败不自动重试：

| case | 实际验证 | 观察结果 |
| --- | --- | --- |
| `catalog` | GetUserStatus、GetCliModelConfigs、渠道模型发现 | plan 为 Pro；目录保留 SWE-2/Flash 的 medium/high/max 及 SWE-2 lightning 变体；发现列表包括可用 high 对应的裸名别名，没有其他模型家族 |
| `flash_stream` | `swe-2-flash` → `swe-2-flash-high`，OpenAI SSE | 返回 LIVE_OK，正常结束；prompt 398 / completion 24 |
| `swe_nonstream` | `swe-2` → `swe-2-high`，非流式 completion | 返回 LIVE_OK；prompt 469 / completion 40 |
| `claude` | Claude Messages 请求转换与流式响应桥接 | 返回 LIVE_OK，正常结束；prompt 398 / completion 29 |
| `responses` | Responses 请求转换与流式响应桥接 | 返回 LIVE_OK，正常结束；prompt 398 / completion 30 |
| `tool_required` | 强制工具调用 | 返回 report_color 工具调用；prompt 447 / completion 15 |
| `tool_named` | 指定 report_color 函数 | 返回指定工具调用；prompt 447 / completion 15 |
| `tool_none` | 禁止工具调用 | 返回 LIVE_OK，无 tool_calls 增量；prompt 404 / completion 34 |
| `image_data` | SWE-2 接收生成的纯红 PNG data URI | 回答 red；prompt 497 / completion 58 |
| `image_url` | 网关下载临时本地 URL 的纯红 PNG，再转发真实上游 | 回答 red；prompt 497 / completion 43，cached 64 |

URL 图片 fixture 仅在隔离测试进程中允许本地 HTTP 下载，生产文件服务的 SSRF 配置不变。以上用量是本次独立观测，不是固定计费参数。账户没有可用的 durable `cog_*` key，故不声明真实 mint/刷新已验证；其持久化、失败和竞争路径已由前节三数据库 fixture 验证。交互式 OAuth 授权与换码仍需浏览器登录覆盖。未故意触发真实账号限流或配额耗尽；异常 trailer 和截断依赖本地协议回归。
