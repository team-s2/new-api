# 本 fork 相对上游的改动与 rebase 手册

本文件记录 `team-s2/new-api` 相对上游 `QuantumNous/new-api` 的全部增量改动，供每次 rebase 时对照使用。
新增功能前请先更新本文件；rebase 完成后请校正基线信息与清单。

## 基线

| 项目 | 值 |
| --- | --- |
| 当前上游基线 | `v1.0.0-rc.48`（2026-11-04） |
| 上一基线 | `v1.0.0-rc.40`（2026-09-21，由 `git merge-base` 精确确定） |
| fork 自有提交数 | 12（不含上游提交） |
| 上游版本号记录位置 | `VERSION`（上游该文件为空，由 CI 在构建时写入；本 fork 填上游 tag） |

> `VERSION` 可能滞后于真实基线，判断基线请用 `git merge-base`，不要只信 `VERSION`。

## 提交清单

按 rebase 后的提交顺序（自旧到新）。「rebase 风险」列说明该提交与上游的耦合点。

| 提交 | 内容 | 主要文件 | rebase 风险 |
| --- | --- | --- | --- |
| `chore(ci): action only build docker and push to ghcr` | 只构建并推送 GHCR 多架构镜像；删除上游的 release / electron / 分支镜像工作流 | `.github/workflows/docker-build.yml`（新增 tag `latest`、`<short-sha>`、`<arch>`）、删除 `release.yml`、`electron-build.yml`、`docker-image-branch.yml`、`VERSION` | 高：上游持续改动被删工作流 → modify/delete 冲突；须保留 fork 的 `docker-build.yml` 与删除，并按上游新增工作流（如 `ci.yml`、`sync-release-to-gitcode.yml`）保留 |
| `fix(relay): recognize Responses stream completion before client disconnect` | 客户端在终态后断开不再被判失败；保留「流式图像数据不落调试日志」 | `relay/helper/stream_scanner.go` 等 | **低**：上游 rc.48 已原生实现终态分类、写失败上报、cleanup 读错误忽略 → 冲突时基本直接采用上游，只保留图像日志那一处 |
| `feat(channel): add BigModel Coding Plan support` | 新渠道类型 `100`（BigModel Coding Plan）：OAuth 登录、额度查询、重置 | `constant/channel.go`、`relay/channel/zhipu_4v/credential.go`、`controller/zhipu_oauth.go`、`controller/zhipu_coding_plan_usage.go`、`service/zhipu_coding_plan_*`、`router/channel-router.go`、`docs/ai/2609051908-bigmodel-coding-plan.md`、前端渠道弹窗 | 中：新增渠道类型必须与前端 `constants.ts` 编号一致；上游新增渠道类型会改同一常量块 |
| `feat(data): aggregate short flow windows from logs directly` | 短窗口流量统计直接聚合 `logs` 表，避免 `quota_data` 整点截断误差 | `model/usedata_flow.go`、`controller/usedata.go`、`common/constants.go`（`FLOW_DIRECT_LOGS_WINDOW_SECONDS`，默认 6h，`0` 关闭） | 中：上游已用可配置的 `limitLogTimeRange`（`log_setting.max_range_days`，默认 365 天）取代 fork 原先硬删 30 天上限 → **采用上游** |
| `feat(auth): restrict GitHub OAuth access and configure role grants` | GitHub OAuth 组织 / 永久用户 ID 白名单与自动角色提升 | `common/github_access.go`、`model/github_access.go`、`oauth/github_access.go`、`controller/oauth.go`、`service/auth_session.go`、web 系统设置与 7 个前端 locale、`docs/ai/2610040349-github-oauth-access.md` | **高**：上游把 `oauth.OAuthError` 改为 `*common.Message`（无 `MsgKey`/`Params`），web console 文案改为英文源文本 + 前端翻译。fork 的 403 语义用独立类型 `oauth.GitHubAccessDeniedError` 保留 |
| `feat(channel): support grok subscription (#3)` | 新渠道类型 `101`（Grok Subscription）：适配器、OAuth、计费 | `constant/channel.go`、`relay/channel/groksub/`、`controller/grok_oauth.go`、`service/grok_oauth.go`、`service/grok_billing.go`、前端弹窗 | 中：渠道类型编号与 `relay_adaptor.go` 注册点 |
| `fix(channel): correct grok subscription port and add usage probe` | Grok 端口修正与用量探测 | `relay/channel/groksub/`、`service/grok_credential_refresh.go`、`router/channel-router.go`（`/grok/refresh`、`/grok/usage`） | 中：与上一提交同区域，rebase 时两个提交可能连续冲突 |
| `fix(auth): persist Grok OAuth sessions across workers` | Grok OAuth 令牌跨 worker 持久化 | `service/grok_oauth.go`、`service/grok_credential_refresh*.go` | 低 |
| `docs: update AGENTS.md and add rebase handbook` | 写入本 fork 的变更摘要、维护流程与开发约束；新增本手册 | `AGENTS.md` 顶部 fork 段、`docs/ai/2611070134-fork-rebase-handbook.md` | 中：上游对 `AGENTS.md` 改动很大，冲突时保留 fork 摘要块，其余用上游 |
| `fix: 仅在协议成功后写入渠道亲和并记录流错误分类` | 渠道亲和仅在协议成功后建立/续期；日志记录流错误分类 | `service/channel_affinity.go`、`service/log_info_generate.go`、`service/request_policy.go`、`relay/common/stream_status.go`（`IsSuccessful`） | **高**：见下方「测试契约变更」 |
| `fix: 在 WebSocket 终止结果后记录亲和并修正回归测试` | WebSocket 在终态后写亲和；修正亲和相关回归测试 | `relay/responses_websocket.go`、`service/log_info_generate.go`、`controller/channel_pin_retry_test.go`、`relay/responses_websocket_test.go` | 中：上游新增了亲和测试，断言「分发时即改绑」，与 fork「协议成功后改绑」不一致 |
| `feat(channel): route Coding Plan channels through the ZCode ultra endpoint` | 类型 100 默认 base 改为 `zcode-ultra`：Claude 协议走 ultra 路由（官方 1.5x 额度），OpenAI 协议与模型列表仍走直连 coding 端点 | `constant/channel.go`（新增 `zcode-ultra`、`zcode-ultra-international` 预设与类型 100 默认值）、`docs/ai/2609051908-bigmodel-coding-plan.md` | 低：只新增预设键与默认值；上游若调整 `ChannelSpecialBases` 结构或默认 base 数组需同步 |
| `feat(channel): disable Zhipu Coding Plan channels during peak hours` | 智谱 Coding Plan 渠道高峰期自动禁用与恢复，QQ 机器人 `peak` 命令和群提示；统一 Coding Plan 渠道判定 | `constant/channel.go`（`IsZhipuCodingPlanBase`、`IsZhipuCodingPlanChannel`）、`relaykit/dto/channel_settings.go`（`peak_auto_disable`）、`model/zhipu_coding_plan_peak.go`、`model/channel.go`（清理保护）、`service/zhipu_coding_plan_peak.go`、`controller/channel-test.go`（巡检不复活）、`qqbot/peak.go`、`main.go`、前端渠道抽屉与 7 个 locale | 中：依赖渠道 `setting` JSON 与 `other_info.status_reason`；上游若重构渠道状态写入（`model.UpdateChannelStatus`）或渠道设置结构需同步 |

### 已移除的改动（2026-11-07，owner 决定）

以下两个 Codex 相关提交**不在**当前历史中，上游 rc.48 也没有对应实现：

- `feat(codex): support gpt-image-2 image endpoints`（Codex 渠道图像生成/编辑中继、校验、流式与计费防护）
- `feat(codex): impersonate native codex-tui client for chatgpt.com backend`（uTLS Chrome 指纹、codex-tui UA/originator、session id 与 `prompt_cache_key` 联动）

恢复方式：`git cherry-pick` 原始提交（原始 SHA `7dfe4f9e8`、`294f597a3`）。rebase 前的历史保留在远端备份分支 `backup/main-261107`（= `294f597a3`，即 rc.40 基线时期的 fork main）与 `backup/main-before-rc40`。恢复时需按上游最新的 Relay / 计费契约重新适配。

## 配置与数据影响

- **渠道类型编号**：`100` = BigModel Coding Plan，`101` = Grok Subscription。`64-99` 预留给上游新增渠道类型，新功能请从 `102` 起用。持久化编号必须与前端 `web/src/features/channels/constants.ts` 一致。
- **Option**：`GitHubAccessPolicy`（JSON：`enabled` / `user_ids` / `organizations` / `role`），写入前经 `common.ParseGitHubAccessPolicy` 校验。
- **渠道端点预设**（`constant.ChannelSpecialBases`）：`zcode-ultra`（类型 100 默认，Claude 协议走 ultra，OpenAI/Responses/模型列表走直连）、`zcode-ultra-international`、`glm-coding-plan`、`glm-coding-plan-international`。ultra 路由按官方 1.5x 额度计量，仅提供 Anthropic Messages 协议。
- **环境变量**：`FLOW_DIRECT_LOGS_WINDOW_SECONDS`（默认 `21600`，`0` 关闭直读 logs 的短窗口路径）。
- **数据库**：fork **未新增表、未改动 `AutoMigrate`**。GitHub 授权信息复用 `users` / `user_sessions` / `auth_flows`；Grok / BigModel 凭据以 JSON 存入渠道 `key`；短窗口统计直读 `logs`；高峰期自动禁用复用渠道 `setting`（`peak_auto_disable`）与 `other_info.status_reason`。因此 rebase 不需要数据迁移验证，但仍需保证上游迁移脚本可正常执行。
- **后台任务**：`service.StartGrokCredentialAutoRefreshTask()`（`main.go`），节奏与 Codex 一致；`service.StartZhipuPeakTask(qqbot.NotifyZhipuPeak)`（每 30 秒，仅 master），按高峰期切换 Coding Plan 渠道状态。
- **HTTP 路由新增**（`router/channel-router.go`）：`/:id/zhipu/coding-plan/{usage,reset-status,reset}`、`/zhipu/oauth/{login,exchange}`、`/grok/oauth/{login,exchange}`、`/:id/grok/{refresh,usage}`。

## rebase 操作手册

```bash
# 1. 确认当前基线（不要依赖 VERSION）
git merge-base origin/main <upstream-tag>

# 2. 取上游最新 release tag
git ls-remote --tags https://github.com/QuantumNous/new-api.git

# 3. 变基（<旧tag> 为 merge-base 命中的上游提交/标签）
git fetch upstream <new-tag> <old-tag>
git rebase --onto <new-tag> <old-tag> main

# 4. 更新 VERSION 为上游 tag，同步本文件与 AGENTS.md 的基线

# 5. 验证（见下）后 force push
```

### 冲突决策原则

1. 上游已实现 fork 的改动 → **直接用上游实现**，删除 fork 版本。
2. 上游改了契约（错误模型、日志 API、配置系统）→ **适配 fork 实现**，不要回退上游。
3. fork 独有的语义（403、权限白名单、渠道编号）→ 保留，必要时用独立类型/常量隔离，避免与上游类型共享字段。

### 常见冲突文件与本次处理

| 文件 | 本次结论 |
| --- | --- |
| `AGENTS.md` | 保留 fork 摘要块，其余采用上游 |
| `VERSION` | 上游为空文件；fork 写上游 tag（如 `v1.0.0-rc.48`） |
| `Dockerfile` | 采用上游（fork 不改动此文件，不在构建期跑测试） |
| `controller/relay.go` | 上游的 `writeRelayStreamError` + fork 的「已写入非 SSE 响应则直接返回」合并 |
| `relay/helper/common.go`、`relay/helper/stream_scanner.go`、`relay/channel/openai/relay_responses.go` | 全部采用上游（已覆盖 fork 意图） |
| `controller/usedata.go` | 采用上游 `limitLogTimeRange`（可配置，默认 365 天） |
| `i18n/keys.go`、`i18n/locales/*.yaml` | 上游重构为「AI 客户端/邮件文案」专用，web console 文案改英文源文本 → fork 旧 key 删除，改在前端 locale 增加 |
| `go.mod` / `go.sum` | 保留上游新增模块（如 `tokenkit`）与 fork 依赖，禁止只保留一方 |
| 前端 `web/src/i18n/locales/*.json` | 见下方 i18n 规则 |

### 测试契约变更（易踩）

- `service.RecordChannelAffinity` 现在要求请求策略已记录**协议成功**（`MarkRequestPolicySuccess`），否则静默不写。上游新增的亲和测试会直接调用 `RecordChannelAffinity`，rebase 后需在测试里先补 `service.MarkRequestPolicySuccess(ctx, nil)`，或采用上游更新的测试写法。
- 流日志的 `stream_status.status` 采用 fork 条件：非正常结束、有错误、`response.failed`，或「要求终态但缺失终态」才算 `error`；`incomplete` / `cancelled` 属正常结束。改动此处会同时影响上游 `TestOaiResponsesStreamHandlerCompletionLifecycle`。
- 上游 `MarkRequestPolicySuccess` 内的成功判定已被 fork 收敛到 `StreamStatus.IsSuccessful()`，修改时保持两处一致。

### 前端 i18n 规则

- 只改 `web/src/i18n/locales/en.json`（新增英文键），然后从 `web/` 运行 `bun run i18n:sync` 把键按 **en 的键顺序** 传播到其余 6 个 locale，再填译文。
- 不要自写「按字母排序」的脚本重写 locale：本仓库顺序以 `en.json` 为基准，自排会产出数百行无意义 diff。

## 验证清单

```bash
# 后端
go build ./...
go test ./...
(cd relaykit && GOWORK=off go build ./...)
(cd tokenkit && GOWORK=off go build ./...)

# 前端（在 web/ 下，bun 1.4.x）
bun install
bun run typecheck
bun run lint
bun run test
```

已知环境注意：

- `web/dist` 是 `go build` 的 embed 目标，未构建前端时先放占位文件，否则 `main` 包报 `pattern web/dist: no matching files found`。
- `pkg/jsplugin` 测试会下载仅测试用依赖，`proxy.golang.org` 不可达时用 `GOPROXY=https://goproxy.cn,direct`。
- WSL / 容器里若设置了 `HTTPS_PROXY`，`service` 包的指纹拨号与直连超时测试会误判失败，跑测试前 `unset HTTPS_PROXY https_proxy HTTP_PROXY http_proxy ALL_PROXY all_proxy`。
