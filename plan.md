• ## 结论

当前项目适合作为二次开发的“中转网关核心”，但不适合不改代码就直接充当完整的用户额度管理平台。

大致判断：

- 作为多供应商统一网关：8/10
- 作为 Access Key 管理系统：6/10
- 作为用户额度/账本系统：3/10
- 作为二开基础：8/10

项目已经解决了最复杂的一部分：供应商协议适配、统一 API、流式响应、用量提取、成本计算和上游密钥隐藏。你主要需要补齐账户、额度账本、硬限额和按用户路
由策略。

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

但是，当前有四个关键问题：

1. onr:v1 Token 没有签名，p 是客户端可修改参数，见 onr/internal/auth/token.go:44。所以它适合“允许用户自己选择供应商”，不适合“管理员动态控制供应
    商”。

2. 普通 Access Key 用户也可以发送 x-onr-provider。目前没有 Access Key 级供应商白名单。
3. Redis 中虽然有 SubjectID，但认证后实际返回的是 record.Name，见 onr/internal/onrserver/router.go:93。也就是说，当前计费主体实际上是 Access Key 名
    称，而不是记录里的 SubjectID。如果两者不同，汇总结果会不符合预期。

4. 额度扣减依赖外部 Meterry。ONR 本地没有完整账本，也没有原子预占/扣减。当前模式是请求前查询余额、请求后异步发送用量事件，见 onr/internal/
    onrserver/billing.go:58。高并发情况下可能出现短暂超额。

现阶段可以用以下约束实现 MVP：

- 一个 Access Key 对应一个账户
- Access Key 的 name 和 subject_id 设置为相同值
- 启用 pricing、Redis、Meterry 和 balance enforcement
- 禁止向不可信用户开放 x-onr-provider
- 不使用客户端可修改的 onr:v1&p=... 管理供应商

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
用户管理                                     低    目前只有 Access Key 记录，没有完整用户实体
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

由于该项目强调 DSL 驱动，路由能力应做成显式、原子的 DSL 指令，并在以下位置同时完成解析、验证、文档和测试：

- onr-core/pkg/dslconfig
- DSL_SYNTAX.md
- config/providers

Access Key 在 Redis 中只保存 route_policy_id，不要把大量隐式供应商判断写进 onr/internal/proxy。

### 3. 建立权威额度账本

如果不想依赖 Meterry，需要增加自己的账本服务。最小数据模型建议包括：

- accounts
- access_keys
- wallets
- ledger_entries
- usage_events
- route_policies
- quota_policies

一次请求应采用：

认证
→ 检查账户状态
→ 估算并预占额度
→ 选择供应商
→ 发起请求
→ 提取实际用量和成本
→ 按 request_id 幂等结算
→ 释放多余预占

仅仅在请求前检查“余额是否大于 0”并不够。并发请求可能同时通过检查，因此必须使用数据库事务或 Redis Lua 做原子预占。

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

- 用户/账户创建、禁用
- 充值和额度调整
- Access Key 创建、轮换、吊销
- 为 Access Key 修改路由策略
- 当前余额、累计消费
- 按日/月/provider/model 汇总
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

第三阶段实现自有账本：

1. 原子额度预占和结算。
2. 日/月额度及限速。
3. 用户消费看板。
4. 对账和异常用量修正。

最终建议是：保留 ONR 作为纯粹的 DSL 驱动网关执行层，把用户、账户、额度和账本做成独立控制面。这样最符合当前项目架构，也能避免把计费规则和用户状态硬
塞进代理代码。

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
- [ ] Step 4: add an authoritative ledger only if the product later requires stricter accounting.

Verification note: `gofmt` and `go test ./...` passed with Go 1.26.6 using `/tmp` Go caches.
