# 智谱 Coding Plan OAuth 渠道

本 fork 的 `BigModel Subscription (Coding Plan)` 渠道使用类型 **62**，通过 OAuth 登录取得包含 `api_key` 和 `access_token` 的 JSON 凭据。`api_key` 用于模型调用，`access_token` 用于订阅用量查询。普通智谱 V4 渠道（类型 26）配合 `glm-coding-plan` 地址仍是独立的接入方式。

## 类型编号兼容性

类型 **61** 属于上游 Task Plugin，不能重用。前后端新增 Coding Plan 功能时应分别使用 `ChannelTypeBigModelSub` 和 `CHANNEL_TYPE_BIGMODEL_SUB`，避免显示名称、OAuth 控件、凭据校验、订阅用量和实际中继使用不同编号。

早期 fork 曾把 Coding Plan 写入类型 61。后续只修正了类型名称和后端编号，部分前端逻辑仍检查 61，导致原渠道显示为 Task Plugin，而真正的 Coding Plan 缺少 OAuth 和用量控件。当前版本已统一。

升级旧 fork 时：

1. 类型已为 62 的 Coding Plan 渠道无需迁移。
2. 确认旧类型 61 渠道实际属于 Coding Plan 后，在编辑界面选择 Coding Plan 并保存；不需要更换凭据时将密钥输入留空。更新请求会省略密钥，后端保留已有 JSON。
3. 不要将所有类型 61 的数据库记录批量修改为 62；真正的 Task Plugin 必须保留原类型。程序不通过猜测凭据内容自动转换渠道。

审计日志中的 `changed_fields` 能确认一次编辑是否更换了密钥，但不保存旧密钥内容。已被覆盖或删除的凭据不能仅凭审计日志还原，需要有效备份或重新 OAuth 登录。
