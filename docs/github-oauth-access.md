# GitHub OAuth 登录白名单

本 fork 在「系统设置 → 身份验证 → OAuth 集成 → GitHub」中增加登录白名单和自动角色配置。

## 配置

1. 配置 GitHub OAuth App 的 Client ID、Client Secret 和回调地址，启用 GitHub OAuth。授权请求显式携带当前浏览器站点地址加 `/oauth/github` 作为 `redirect_uri`，该地址必须被 OAuth App 接受；例如本地验证为 `http://127.0.0.1:6185/oauth/github`，生产为 `https://new-api.zjusec.net/oauth/github`。仅修改 new-api 的站点地址不会修改 GitHub App 的回调配置。
2. 如果需要首次登录时创建账号，启用注册（`RegisterEnabled`）。可以单独关闭密码注册（`PasswordRegisterEnabled`）。
3. 打开「限制 GitHub 登录」，填写允许的组织名称或 GitHub 用户数字 ID，支持逗号、空白或换行分隔。
4. 选择自动授予的角色并保存。

组织和用户之间是 **或** 的关系：属于任一指定组织的正式成员，或者账号 ID 在用户白名单中，即可通过检查。待接受的组织邀请不算正式成员。开启限制但两个列表均为空时，拒绝所有 GitHub 登录。

用户必须填写永久数字 ID，而非可变的用户名。在 `https://api.github.com/users/USERNAME` 中查看 `id` 字段。这样即使用户改名、原用户名被其他账号注册，也不会把登录权限转移给其他人。

OAuth 授权请求包含 `user:email read:org`。启用第三方应用访问限制的组织，需要组织管理员批准 OAuth App。已有用户可能需要重新授权。如果 GitHub 无法确认任一匹配关系，则拒绝访问，不因网络错误、限流或权限不足而放行。

## 自动角色

| 配置 | 行为 |
| --- | --- |
| 保留现有角色 | 已有用户保持角色，新用户为普通用户 |
| 管理员（10） | 登录成功后，普通用户提升为管理员 |
| 超级管理员（100） | 登录成功后，普通用户和管理员提升为超级管理员，拥有完整权限 |

自动提升只在白名单开启时生效，对新账号和已有账号均适用。不会降级已有的更高角色；关闭自动提升也不会撤销此前授予的角色。绑定 GitHub 或使用 GitHub 进行敏感操作验证时会检查白名单，但不会自动提升角色。

提升在完成全部登录因素之后执行，不绕过已有的 TOTP 或 Passkey 验证。角色更新和新会话创建在同一事务中完成，并更新认证版本与缓存，旧会话不能继承新授予的权限。自动提升会记录安全审计事件。

## 生效范围

- 升级后默认不启用限制，必须在上述界面保存具体白名单。
- 白名单对内置 GitHub OAuth 的登录、绑定和验证生效，已有管理员和超级管理员也不能绕过检查。
- 密码、Passkey、其他 OAuth 提供方等登录方式不受此白名单限制，可保留本地超级管理员作为恢复入口。
- 组织身份在 GitHub 回调时检查；需要第二因素时，检查结果绑定到最长五分钟的登录挑战。挑战期间配置发生变化，会拒绝旧挑战，要求重新登录。
- 组织变动和白名单更新不会主动撤销已有会话、API 令牌或其他登录方式。移除成员访问权时，应另外撤销会话、令牌并调整账号状态和角色。
- 白名单不会绕过已禁用账号、注册开关或会话数量限制。

配置通过现有 options 存储为单个 `GitHubAccessPolicy` JSON 值，避免列表和角色分开保存时出现中间状态；不新增数据库字段。示例：

```json
{"enabled":true,"organizations":["team-one","team-two"],"user_ids":["123456"],"role":100}
```

## 安全设计与验证

参考 OWASP ASVS **5.0.0** 的 8.1.1（授权规则文档）、8.3.1（服务端授权检查），以及以下指南：

- [Authentication Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Authentication_Cheat_Sheet.html)
- [Session Management Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Session_Management_Cheat_Sheet.html)
- [OAuth 2.0 Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/OAuth2_Cheat_Sheet.html)
- [CSRF Prevention Cheat Sheet](https://cheatsheetseries.owasp.org/cheatsheets/Cross-Site_Request_Forgery_Prevention_Cheat_Sheet.html)

回归测试覆盖组织或用户匹配、拒绝未列出的新老账号、空列表、待接受邀请、错误响应、无效 OAuth state、第二因素完成前不提升权限、过期／重放挑战、配置或绑定变化、禁用账号、角色保留和 Redis 缓存会话失效。数据库测试支持真实 SQLite、MySQL 和 PostgreSQL，通过 `TEST_MYSQL_DSN`、`TEST_POSTGRES_DSN` 指向专用测试数据库后执行：

```sh
go test ./common ./oauth ./service ./controller -run TestGitHub -count=1
```

GitHub 外部响应在自动化测试中模拟；上线后仍需使用实际成员和非成员账号验证 OAuth App 的组织授权。这是本功能的针对性检查，不代表对整个应用进行 ASVS 合规认证。

### 本次验证记录

在容器中使用 Go 1.26.1、Bun 1.4.0 完成以下检查：

- 设置 `TEST_MYSQL_DSN`、`TEST_POSTGRES_DSN` 指向独立测试数据库后，运行 `go test ./common ./oauth ./model ./service ./controller -run 'TestGitHub|TestAuthFlow|TestCreateLoginSession|TestAccessToken|TestRefresh' -count=1`，全部通过。实际数据库为 SQLite 3.50.4、MySQL 8.0.46、PostgreSQL 16.15；覆盖角色更新、事务、MFA、权限配置持久化及旧会话失效。
- `go vet ./common ./oauth ./model ./service ./controller`、`go build ./...` 通过。
- 在 `web/` 运行 `bun run test src/lib/__tests__/github-oauth.test.ts src/features/system-settings/auth/__tests__/oauth-settings.test.tsx src/features/system-settings/auth/__tests__/github-access-policy.test.ts`，28 项通过；`bun run typecheck`、受影响文件 lint／format 检查、`bun run build` 通过。
- 使用隔离数据库启动修改后的完整实例，用户完成真实 GitHub 登录并确认功能正常。未授权用户拒绝路径由自动化测试覆盖；生产 OAuth App 的回调和组织授权仍需按部署环境配置。
