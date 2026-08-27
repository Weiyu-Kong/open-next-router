• ## 结论

当前项目适合作为二次开发的“中转网关核心”，但不适合不改代码就直接充当完整的用户额度管理平台。

大致判断：

- 作为多供应商统一网关：8/10
- 作为 Access Key 管理系统：6/10
- 作为用户额度/账本系统：3/10
- 作为二开基础：8/10

项目已经解决了最复杂的一部分：供应商协议适配、统一 API、流式响应、用量提取、成本计算和上游密钥隐藏。后续重点是把 Access Key 做成用户身份和额度入口，并补齐用户查询门户、管理员额度操作以及供应商延迟用量同步。

## 最终产品目标

ONR 最终定位为“多供应商中转网关 + Access Key 额度管理站”。当前 MVP 不建立独立的用户实体，Access Key 同时承担 API 调用凭证、用户登录凭证和额度归属标识。

目标使用方式：

```text
管理员创建 Access Key 并设置初始额度
        ↓
用户使用 Access Key 登录额度门户
        ↓
用户通过中转站地址调用 OpenAI-compatible API
        ↓
ONR 选择供应商、隐藏上游信息、提取请求用量并累计到该 Access Key
        ↓
用户查看余额、请求记录、模型用量和时间段统计
```

四条必须同时成立的产品主线：

1. **多供应商适配和计费**
   - 支持 OpenAI chat completions、responses，以及其他已配置的协议入口。
   - 每次请求提取 input/output/cache 等用量，计算费用并累计到 Access Key 对应的计费账户。
   - 对于无法从单次响应获得完整用量的供应商，支持通过供应商 usage API 按时间段、内部 API key 或请求 ID 补采和校准。
   - 单次事件和补采事件必须使用稳定的 request ID/idempotency key，避免重复计费。

2. **中转和信息隔离**
   - 用户只需要 Access Key 和 ONR 中转地址即可发起请求。
   - 上游供应商 API key、内部 key 名称、供应商凭据文件和内部路由细节不得返回给用户。
   - 上游错误需要转换为统一错误，不能泄漏供应商 URL、内部 key、认证头或内部堆栈。
   - Provider 选择可以由管理员路由策略决定；客户端传入的 provider 不能绕过 Access Key 策略。

3. **管理员控制**
   - 创建、查询、禁用、吊销、轮换 Access Key。
   - 设置初始额度、充值、扣减、余额调整和使用限制。
   - 管理 provider、内部 API key、模型路由、价格和用量提取规则。
   - 管理员可以查看所有 Access Key 的余额、消费汇总、失败请求和账单明细。
   - 钱包和账本操作以 Meterry 为权威，ONR Redis 只保存访问控制、缓存、队列和非敏感映射。

4. **Access Key 用户门户**
   - 用户使用 Access Key 登录，不需要额外的用户账号体系。
   - 用户只能查看当前 Access Key 对应账户的数据。
   - 支持余额、可用余额、初始额度、限制状态、请求记录和消费金额。
   - 支持按模型统计，并支持小时、天、周粒度和自定义时间范围。
   - 用户侧默认不展示供应商和内部 key 信息；如需展示 provider，必须经过产品层明确授权和脱敏。

## 你的核心问题

每个 Access Key 可以动态切换供应商，并汇总额度消费吗？

技术上可以，但当前实现有重要限制。

目前同一个 Access Key 可以通过以下方式切换供应商：

- 请求头 x-onr-provider
- onr:v1?...&p=openai 形式的 Token Key
- 根据 models.yaml 为模型配置多个供应商并轮询

供应商选择入口位于 onr/internal/onrserver/handlers.go:61，模型多供应商轮询位于 onr-core/pkg/models/models.go:64。

每次请求产生的计费事件包含：

- Access Key 对应的主体
- provider
- model
- API
- input/output/cache tokens
- pricing hints
- request ID

相关事件结构见 onr/internal/meterry/event.go:10。

因此，只要不同供应商的请求使用同一个计费主体，理论上可以汇总到同一账户。

但是，当前仍有三个关键问题：

1. onr:v1 Token 没有签名，p 是客户端可修改参数，见 onr/internal/auth/token.go:44。所以它适合“允许用户自己选择供应商”，不适合“管理员动态控制供应
    商”。

2. Access Key 级 provider/model 白名单和 route policy 已经加入，但当前还需要把默认 provider 选择、用户可见错误和管理配置进一步收紧，确保客户端不能绕过管理员策略。
3. Redis 中虽然已经保存 `account_id`，但 Meterry account、wallet 和 subject 的正式初始化及查询门户尚未完成。当前计费仍主要依赖 `subject_type`/`subject_id` 和异步事件上报。

额度扣减依赖外部 Meterry。ONR 本地没有完整账本，也没有原子预占/扣减。当前模式是请求前查询余额、请求后异步发送用量事件，见 onr/internal/
onrserver/billing.go:58。高并发情况下可能出现短暂超额。

现阶段可以用以下约束实现 MVP：

- 一个 Access Key 对应一个账户
- Access Key 的 name 和 subject_id 设置为相同值
- 启用 pricing、Redis、Meterry 和 balance enforcement
- 只允许 Access Key 使用管理员配置的 provider/model/route policy
- 不使用客户端可修改的 onr:v1&p=... 绕过管理员供应商策略

但这仍不是严格的硬额度系统。

## 现有能力匹配情况

诉求                                 当前支持度    说明
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━  ━━━━━━━━━━━━  ━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
对接多个 API 供应商                          高    当前有 25 个 provider DSL 配置
───────────────────────────────────  ────────────  ───────────────────────────────────────────────
统一 Base URL                                高    提供 OpenAI、Claude、Gemini 风格统一入口
───────────────────────────────────  ────────────  ───────────────────────────────────────────────
隐藏供应商 API Key                           高    上游 Key 由 keys.yaml/环境变量管理
───────────────────────────────────  ────────────  ───────────────────────────────────────────────
Access Key 创建、吊销、轮换、过期          较高    Redis 控制面和管理端已经具备
───────────────────────────────────  ────────────  ───────────────────────────────────────────────
同模型动态选择供应商                       中高    支持显式 provider 或全局模型轮询
───────────────────────────────────  ────────────  ───────────────────────────────────────────────
按 Access Key 汇总用量                       中    需要启用外部计费，并修正主体传播
───────────────────────────────────  ────────────  ───────────────────────────────────────────────
按 Access Key 分配额度                       低    本地没有额度账本
───────────────────────────────────  ────────────  ───────────────────────────────────────────────
严格防止超额                                 低    没有原子额度预占与结算
───────────────────────────────────  ────────────  ───────────────────────────────────────────────
用户登录                                     低    当前只有管理员 token 登录，没有 Access Key 用户门户
───────────────────────────────────  ────────────  ───────────────────────────────────────────────
用户用量/余额看板                            低    管理端主要展示健康状态、Access Key 和计费队列
───────────────────────────────────  ────────────  ───────────────────────────────────────────────
限速、并发限制、日/月额度                    低    未发现账户级实现

此外，管理界面已经明确采用“Meterry owns the ledger”的设计，因此 Redis 在当前架构中只是 Access Key 控制面、余额缓存和事件队列，并不是权威账本。

## 推荐的二开模型

可以让 Access Key 在使用体验上代表用户账户，但内部不要把二者永久绑定为同一个概念。

建议至少保留：

Account
└── AccessKey
        ├── account_id
        ├── route_policy_id
        ├── status / expires_at
        └── rate/quota policy

MVP 阶段允许一个账户只有一个 Access Key；以后需要密钥轮换、多设备、多项目 Key 时，不必重构整个账本。

### 1. 修正认证主体

认证后应传播完整主体：

AuthPrincipal {
    AccessKeyID
    AccountID
    SubjectType
    SubjectID
    RoutePolicyID
}

首先需要修正 onr/internal/onrserver/router.go:98，使用 record.SubjectType 和 record.SubjectID，而不是只返回 record.Name。

### 2. 增加管理员控制的供应商策略

不建议继续依赖客户端提交的 x-onr-provider 或 onr:v1&p=。

建议 Access Key 关联一个路由策略，例如：

standard-user:
gpt-4o-mini -> openai, openrouter
claude-*    -> anthropic
fallback    -> siliconflow

运行时按以下顺序选择：

Access Key → route policy → model route → provider → upstream key pool

由于该项目强调 DSL 驱动，路由能力和供应商 usage API 能力都应做成显式、原子的 DSL/config 指令；不能在 `onr/internal/proxy` 中通过 path、api 或 model 名称隐式猜测。新增指令必须在以下位置同时完成解析、验证、文档和测试：

- onr-core/pkg/dslconfig
- DSL_SYNTAX.md
- config/providers

Access Key 在 Redis 中只保存 `route_policy_id`，不要把大量隐式供应商判断写进 `onr/internal/proxy`。

### 3. 使用 Meterry 作为权威额度账本

当前不新增 ONR 自有钱包和账本。Meterry 负责账户、钱包、账单、用量聚合和使用限制；ONR 负责把 Access Key 与 Meterry 的账户和 subject 稳定关联。

Access Key 创建时需要完成：

- 创建或关联 Meterry account。
- 创建对应 currency 的钱包。
- 按配置发放一次初始额度。
- 绑定 ONR 的 `subject_type`/`subject_id` 到 Meterry account。
- 将 provisioning 状态和 Meterry 非敏感 ID 保存到控制面。

如果未来 Meterry 无法满足严格的原子预占、结算或对账需求，再单独评估自有账本，不作为当前 MVP 的前置条件。

一次请求应采用：

认证
→ 检查 Access Key 状态和余额
→ 选择供应商
→ 发起请求
→ 提取实际用量和成本
→ 按 request_id 幂等上报 Meterry

余额检查仍然是最佳努力的请求前允许/拒绝检查，不做额度预占；当余量很少时允许少量超额。

### 4. 完善计费维度

建议每条用量至少记录：

- account_id
- access_key_id
- provider
- upstream_key_name
- model_requested
- model_resolved
- input/output/cache tokens
- actual_cost
- charged_cost
- request_id
- status
- occurred_at

现有 pricing 和 usage extraction 可以直接复用；成本计算入口在 onr/internal/proxy/pricing.go:31。

### 5. 补充管理功能

管理端还需要增加：

- Access Key 初始额度配置、充值和额度调整
- Access Key 创建、轮换、吊销
- 为 Access Key 修改路由策略
- 当前余额、累计消费
- 按小时/天/周及 model 汇总
- 请求明细和失败统计
- 日/月硬额度
- QPS、并发数限制
- 低余额告警

## 推荐实施顺序

第一阶段可以快速上线：

1. 修正 SubjectID 传播。
2. 强制一个 Access Key 对应一个 Account。
3. 增加 Access Key 级 provider/model 白名单。
4. 复用现有用量和 pricing。
5. 使用 Meterry 作为账本，实现基本余额拦截。

第二阶段再实现可控路由：

1. 新增显式 DSL 路由策略。
2. Access Key 关联 route_policy_id。
3. 禁止普通用户直接覆盖 provider。
4. 加入故障切换、权重、供应商健康状态。

第三阶段实现用户额度门户和完整账务流程：

1. Access Key 登录和用户数据隔离。
2. 余额、初始额度和限制查询。
3. 小时、天、周及自定义时间段用量看板。
4. 按模型、provider 和请求的消费明细。
5. 供应商 usage API 补采、幂等去重和对账。

最终建议是：保留 ONR 作为 DSL 驱动的网关执行层，把 Access Key 控制面、Meterry 账务能力和用户查询门户连接起来。不要把供应商兼容逻辑、用户身份或账本状态隐式塞进 `onr/internal/proxy`。

## Implementation Progress

- [x] Step 1: propagate a complete authenticated principal through the request context.
  - Added `auth.AuthPrincipal` with `AccessKeyID`, `SubjectType`, and `SubjectID`.
  - Redis-backed access-key authentication now uses the record's `SubjectType` and `SubjectID`.
  - Legacy Redis records fall back to the access-key name as the subject ID.
  - File-backed access keys and the legacy matcher remain compatible.
  - Meterry events now include the non-secret `access_key_id` in billing metadata.
  - Balance checks now use the authenticated principal subject when present.
- [x] Balance policy clarification: balance checking remains a best-effort pre-request allow/deny check. This iteration does not add atomic quota reservation, strict hard limits, or request-cost preauthorization; a small amount of overspend remains allowed.
- [x] Step 2: add account and route-policy fields to the control plane.
  - Added `account_id` and `route_policy_id` to Redis-backed `AccessKeyRecord`.
  - Propagated both identifiers through `auth.AuthPrincipal` and Meterry event metadata.
  - Added account and route-policy input/display support to the admin CLI, Web UI, and TUI.
  - Access-key migration writes `account_id` explicitly; it defaults to the key name for legacy file-backed keys.
  - Redis writes normalize missing `subject_id` to the access-key name and missing `account_id` to `subject_id`.
  - Redis administrative reads also normalize legacy JSON records in memory, including records without a stored name.
  - `route_policy_id` is metadata only in this step; it is not yet used to authorize provider/model selection.
- [x] Step 3: add provider/model authorization rules per access key.
  - Added `allowed_providers` and `allowed_models` to Redis-backed access keys and authenticated principals.
  - Enforced provider/model allowlists in the OpenAI-style and Gemini request handlers before proxying upstream.
  - Added admin CLI, Web UI, and TUI input/display support for allowlists.
  - Provider allowlists are matched case-insensitively; model allowlists are matched by trimmed exact value.
  - Empty allowlists remain unrestricted for backward compatibility.
- [x] Step 4: keep accounting anchored in Meterry and defer a separate authoritative ledger.
  - This phase does not add a local wallet, reservation, or settlement system in ONR.
  - Balance enforcement remains a best-effort pre-request allow/deny check, and a small amount of overspend is still permitted.
- [x] Step 5: enforce `route_policy_id` against explicit model routes.
  - `onr-core/pkg/models.Router` now exposes configured model routes so access-key policy can be checked before proxying.
  - Requests whose access key policy does not match a model route's `owned_by` are rejected early in the OpenAI and Gemini handlers.
  - Provider overrides are still allowed only when they stay within the configured model route providers.

## User-Facing Product Decisions

The user-facing product uses an Access Key as the user's login credential. A
separate local `User` entity is not required for the current product scope.

- One Access Key represents one user/account.
- `AccessKeyRecord.Name` is the non-secret identifier shown to the user and administrators.
- The plaintext Access Key is accepted only at login or API authentication boundaries; it must never be stored or returned after creation.
- `account_id` is the stable billing identity. For the one-key-per-user MVP, it defaults to the Access Key's account identifier and must map to the corresponding Meterry account.
- `subject_type` and `subject_id` remain the Meterry usage subject fields. The implementation must use one consistent subject mapping for balance checks, usage ingestion, and usage queries.
- Multiple Access Keys under one account are out of scope for the MVP. The control plane should reject a second active key for the same account unless key rotation is being performed.

Each Access Key is assumed to have an initial credit amount when it is created.
The initial credit is authoritative in Meterry, not in ONR Redis.

- Access-key creation must initialize or associate the Meterry account.
- Access-key creation must create or credit the Meterry wallet with the configured initial amount and currency.
- The initial credit operation must be idempotent so retries do not grant the same credit twice.
- ONR must expose the current balance, available balance, credit limit, and currency to the Access Key holder.
- Balance enforcement remains a best-effort pre-request check. It is not an atomic reservation, and a small amount of overspend is allowed by product decision.

The user portal must authenticate with the Access Key and query data only for
that key's account. It must never accept a caller-supplied account or subject
identifier as the authorization source.

## User-Facing Implementation Roadmap

- [x] Step 6: make Access Key the user login credential.
  - Added independent `/api/user/login`, `/api/user/logout`, and `/api/user/me` endpoints to the admin Web server.
  - Login resolves only active Access Keys through the server-side control plane and returns a generic error for invalid credentials.
  - Sessions use an opaque random HttpOnly cookie and are stored server-side with a 12-hour expiry.
  - The user API is mounted outside the administrator Bearer-token middleware; administrator endpoints remain protected separately.
  - Added focused tests for opaque session tokens and unauthenticated user access.
  - Added a five-failure per-client one-minute login throttle; successful login clears the failure counter.
  - Keep the existing admin token flow separate from user access.
  - Add logout, session expiry, brute-force protection, and generic authentication errors.
  - Do not expose the Meterry API key or Redis credentials to the browser.
- [ ] Step 7: provision the Meterry account and initial wallet credit (in progress).
  - Added `initial_credit` configuration and persisted `meterry_account_id`/`provisioning` control-plane fields.
  - Access-key creation now reuses or creates a deterministic Meterry account, binds the subject, creates the currency wallet, and applies the initial credit with an idempotency key.
  - The provisioning path uses Meterry as the authoritative ledger; Redis stores only non-sensitive provisioning metadata.
  - Added administrator credit/debit adjustment endpoint at `/api/admin/access-keys/{name}/balance`; positive decimal amounts and caller-provided idempotency keys are required.
  - Added administrator meter snapshot endpoint at `/api/admin/access-keys/{name}/meter` for the Meterry balance and limits.
  - Added credit/debit controls to the administrator Access Key dialog, including explicit amount, currency, and idempotency-key fields.
  - Added an atomic Redis account index that rejects a second active Access Key for the same account and removes the index on revoke; rotation keeps the account mapping.
  - Added pending provisioning state and administrator retry endpoint at `/api/admin/access-keys/{name}/provision`; retries reuse deterministic Meterry identities and initial-credit idempotency keys.
  - Remaining in this step: production failure-injection tests for partially completed provisioning.
  - Define the deterministic Access Key to Meterry account/subject mapping.
  - Create or bind the Meterry account and subject during Access Key provisioning.
  - Create or credit the initial wallet amount with an idempotency key derived from the Access Key/account creation operation.
  - Add administrator operations for setting the initial amount, crediting, debiting, and correcting an Access Key balance through Meterry.
  - Reject duplicate active Access Keys for the same MVP account and make rotation preserve the account and balance.
  - Store only provisioning status and non-sensitive identifiers in Redis.
- [x] Step 8: add the first server-side Meterry query adapters.
  - Added `meterry.QueryScope` and `meterry.UsageQuery` as server-owned query inputs.
  - Added balance, limits, usage analytics, bill, usage event, and event-detail methods.
  - Query methods require an account or a complete subject scope and derive the project from server configuration.
  - The adapter does not accept browser-supplied project or credential fields.
  - Provider-specific usage adapters and reconciliation remain pending below.
  - Add balance and limit snapshot methods.
  - Add usage analytics and bill query methods.
  - Add usage event detail methods for the user's own account.
  - Add provider usage adapters for suppliers whose authoritative usage is exposed by a separate usage API.
  - Normalize per-request usage, provider-period usage, and reconciliation corrections into one idempotent billing pipeline.
  - Normalize Meterry errors and eventual-consistency state for the portal.
- [ ] Step 9: add user-facing read APIs.
  - Added `/api/user/balance`, `/api/user/limits`, and `/api/user/usage`.
  - Added `/api/user/requests` with a bounded time range and a deliberately reduced response shape.
  - Added `/api/user/bills` with model-level request counts, charge amounts, and metrics.
  - User sessions now revalidate the active Access Key on every protected request, so revocation invalidates existing portal sessions.
  - Balance and limits are read from Meterry through the server-side Access Key account/subject mapping.
  - Usage queries support hour/day/week buckets, a bounded 31-day range, and model grouping.
  - User read endpoints use the authenticated session record and do not accept caller-supplied account or subject identifiers.
  - Remaining in this step: production integration tests against Meterry and explicit currency metadata in the bill response.
  - Add authenticated endpoints for profile, balance, limits, usage summary, time series, and usage details.
  - Derive `account_id`, `subject_type`, and `subject_id` from the authenticated Access Key record.
  - Reject attempts to query another account, subject, or Access Key.
- [ ] Step 10: add usage aggregation views.
  - Support hourly, daily, and weekly buckets.
  - Support custom start/end timestamps with bounded query ranges and an explicit timezone.
  - Support total usage and grouping by model and provider.
  - Return request count, input tokens, output tokens, cache tokens when available, and charged amount.
  - Keep provider and internal-key dimensions available to administrators while suppressing them from the default user response.
  - Show data freshness and pending billing-event information because ingestion is asynchronous.
- [x] Step 11: add the first Access Key user portal.
  - Added `/user` with Access Key login and logout screens.
  - Added current balance display and hourly/daily/weekly usage selector.
  - Added a responsive model usage view backed by the authenticated user APIs.
  - Added a server-side request history API; provider, URL, and internal-key fields are excluded from its user response.
  - Provider breakdown and richer bill detail remain pending until the corresponding server read APIs are added.
  - Keep admin operations and provider configuration out of the user portal.
- [ ] Step 12: verify the complete user journey.
  - Provision an Access Key with an initial credit.
  - Log in using only that Access Key.
  - Confirm the user can see only its own balance and usage.
  - Confirm usage is grouped correctly by model, provider, and time bucket.
  - Confirm direct request usage and provider usage-API reconciliation do not double-charge the Access Key.
  - Confirm upstream provider names, URLs, credentials, and internal key names are absent from user-visible responses and errors.
  - Confirm key revocation blocks both API requests and portal queries.
  - Confirm repeated provisioning requests do not duplicate the initial credit.

Verification note: `gofmt` and `go test ./...` passed with Go 1.26.6 using `/tmp` Go caches.
