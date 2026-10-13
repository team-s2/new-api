# Codex 渠道客户端兼容（chatgpt.com）

本 fork 让 Codex 渠道（ChannelType 57）对 `chatgpt.com/backend-api/codex/*` 的请求完整模拟官方 codex-tui 客户端。参考实现为 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 的 codex executor。本文记录六项能力的设计、配置和验证方式。

## 能力总览

| 能力 | 行为 | 主要代码 |
| --- | --- | --- |
| TLS 指纹 | `https://chatgpt.com` 精确主机名走 Chrome（uTLS `HelloChrome_Auto`）指纹；其余主机走标准 transport | `service/http_fingerprint_transport.go` |
| User-Agent | 全 codex 路径默认 `codex-tui/0.154.0 (...)`，可用环境变量覆盖 | `relay/channel/codex/constants.go` |
| Originator | 全路径统一 `codex-tui`（原 Responses 路径为 `codex_cli_rs`）；客户端值优先 | `relay/channel/codex/adaptor.go` |
| 会话连续性 | 稳定 session id 写入 body `prompt_cache_key` 与上游 `session_id` 头，二者恒一致 | `relay/channel/codex/request_body.go` |
| Body 补齐 | `include`、`image_generation` 工具、`parallel_tool_calls`、`system→developer` | `relay/channel/codex/adaptor.go` |
| 原生头透传 | Version/Thread-Id 等 10 个原生 codex 头从客户端透传 | `relay/channel/codex/request_body.go` |

## TLS 指纹

new-api 的默认 outbound transport 使用 Go 标准库 ClientHello，chatgpt.com 会以 TLS 指纹识别并拒绝。新增的 Chrome 指纹 transport 与 CLIProxyAPI 的 `utlsRoundTripper` 行为一致：

- 每请求独占连接，ALPN 协商 `h2` 时用 `http2.Transport.NewClientConn` 建专用 HTTP/2 连接；协商为空（TLS 检查型代理吞掉 ALPN）或 `http/1.1` 时，以非池化 transport 复用已握手连接。
- 连接随响应体一起关闭（`closeConnectionBody`），指纹连接不做跨请求复用。
- 触发条件是 `https://chatgpt.com` 精确匹配（忽略大小写），子域名和其他主机一律走标准 transport——与 CLIProxyAPI 的 `fallbackRoundTripper` 判定相同。
- 依赖 `github.com/refraction-networking/utls v1.8.2`（仅根模块；`relaykit` 保持独立）。

渠道代理解析顺序：**渠道代理 > 环境变量代理（`HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY`，经 `x/net/http/httpproxy` 现取，与标准 transport 的语义一致）> 直连**。http/https 代理经自写 CONNECT 隧道拨号器（Go 1.25 移除了 `http.NewClientConn`，CONNECT 响应用 `http.ReadResponse` 解析），保证 ClientHello 始终由本进程发出、代理只做字节转发；socks5/socks5h 经 `x/net/proxy`。注意不能用 `proxy.FromEnvironment()` 兜底——它只读 `ALL_PROXY`，会漏掉仅设 `HTTPS_PROXY` 的部署（曾导致线上 codex 全部请求直连黑洞、每次尝试挂 ~135 秒后 500）。TCP 拨号与 TLS 握手各有 15 秒超时（`boundedDialer` / 握手 deadline），黑洞路径快速失败进入正常重试。

WebSocket 路径（`DoWssRequest`）：目标是 chatgpt.com 时，通过 `NetDialTLSContext` 走 Chrome 指纹 + http/1.1-only ALPN 拨号，渠道代理同时生效。

## User-Agent 与 Originator

默认 UA 为 `codex-tui/0.154.0 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.154.0)`。设置环境变量 `CODEX_USER_AGENT` 可覆盖（`common/init.go`），空值保持默认。

优先级：客户端显式传的 `User-Agent`（经原生头透传）> 渠道 `HeadersOverride` > `CODEX_USER_AGENT` > 内置默认。`HeadersOverride` 在 `SetupRequestHeader` 之后应用，仍是最高优先级的运营配置。

Originator 在 Responses/compact/alpha-search/images/wham-usage 全路径统一为 `codex-tui`；客户端传的 `Originator` 头优先。

## 会话连续性

session id 按以下优先级解析（`resolveCodexSessionID`）：

1. 客户端请求头 `Session_id` / `Session-Id`
2. 请求 body 的 `prompt_cache_key`
3. 按 API token 派生的稳定 UUIDv5（`CodexSessionUUID`，namespace OID + `new-api:codex:prompt-cache:<token>`）

该值同时写入：

- 上游 body 的 `prompt_cache_key`（`ConvertOpenAIResponsesRequest`）
- 上游请求头 `session_id`（`SetupRequestHeader`；已有值不覆盖）

与 CLIProxyAPI 的差异：其派生输入是执行会话元数据的多级回退链（Claude 元数据、`X-Client-Request-Id` 等），new-api 无对应概念，改为按 token 派生——同一客户端 token 的所有请求得到同一 session id，不同 token 相互隔离。图片路径原先每请求随机 UUID 的行为已移除。

渠道亲和（channel affinity）的内置规则 `codex cli trace` 本来就以 body `prompt_cache_key` 为 key，因此补齐后的值自动驱动「同会话粘同一渠道」；服务端补齐仅发生在客户端未提供任何会话标识时，不改变客户端自带 `prompt_cache_key` 的语义。

## Body 补齐

`ConvertOpenAIResponsesRequest`（非 compact、非 Responses-Lite）在既有清理（`store=false`、剥离 `max_output_tokens`/`temperature`/两类 penalty）之上补齐 CLI 必带字段：

- `include`：规范化为 `["reasoning.encrypted_content"]`；请求含 web_search 工具或客户端已请求 sources 时为 `["reasoning.encrypted_content","web_search_call.action.sources"]`
- `parallel_tool_calls: true`
- `tools` 追加 `{"type":"image_generation","output_format":"png"}`（已存在 image 工具则跳过）；`*spark` 模型与 Responses-Lite 请求跳过
- input 数组中 `system` 角色改写为 `developer`

Responses-Lite 判定（`isCodexResponsesLiteRequest`）：客户端头 `X-OpenAI-Internal-Codex-Responses-Lite: true` 或 body `client_metadata.ws_request_header_x_openai_internal_codex_responses_lite` 为真。

## 原生头透传

`passthroughCodexNativeHeaders` 在 adaptor 层复制客户端的以下头（仅当客户端已传且上游尚无值；HTTP 头匹配大小写不敏感，覆盖 codex 的下划线拼写）：

`Version`、`Thread_id`、`Thread-Id`、`X-Client-Request-Id`、`X-Codex-Beta-Features`、`X-Codex-Turn-Metadata`、`X-Codex-Turn-State`、`X-Codex-Window-Id`、`X-Openai-Internal-Codex-Responses-Lite`、`X-Responsesapi-Include-Timing-Metrics`

头引用的 codex 上游提交：`7c7b4861`（session/thread 头）、`14df0e88`（metadata 头）、`ebdd8795`（turn-state 往返）。

与渠道亲和 `pass_headers` 模板（`setting/operation_setting/channel_affinity_setting.go` 的 `codexCliPassThroughHeaders`）互补：adaptor 层透传保证即使管理员关闭亲和规则，真实 codex CLI 客户端的头仍然到达上游。两级透传用值相同，不会冲突。

## 验证

```sh
go build ./...
go test ./relay/channel/codex/ ./service/ -count=1
go test ./... -count=1
cd relaykit && GOWORK=off go build ./...
```

回归测试要点（`relay/channel/codex/adaptor_test.go`）：

- CLI 形 body：include/parallel_tool_calls/image 工具/system→developer/prompt_cache_key 与 token 派生 session 一致
- 客户端 `prompt_cache_key` 与 `Session_id` 头优先于服务端派生
- include 的 web_search 变体与 Responses-Lite 豁免
- 头身份：UA/originator/session 默认值 + 6 个透传头
- `CODEX_USER_AGENT` 覆盖生效

TLS 指纹测试（`service/http_fingerprint_transport_test.go`）：本地捕获 ClientHello 字节，断言 Chrome 指纹与 Go 默认 ClientHello 摘要不同；主机匹配规则（精确 chatgpt.com、排除子域名/其他主机/http）；代理解析顺序（env proxy 兜底、渠道代理优先、NO_PROXY 直连，均用本地 CONNECT 探针验证）；拨号超时上限（TEST-NET 不可路由地址在限时内失败）。

本次验证记录（Go 1.25.1 工具链）：`go build ./...`、`go vet ./relay/... ./service/ ./common/`、`go test ./...` 全部通过；`relaykit` 独立构建通过。未做真实 chatgpt.com 联调，上线后建议用真实 OAuth 凭证验证一次 Responses 流式请求与 WebSocket 升级。
