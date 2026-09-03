# ONR 本地计费

ONR 自己维护计费账本，不需要任何外部计费网站。每个 Access Key 对应一个
`account_id`，账户保存初始额度、实际消费和剩余额度。

启用配置：

```yaml
billing:
  enabled: true
  currency: "CNY"
  initial_credit: "200"
redis:
  enabled: true
  addr: "redis://127.0.0.1:6379/0"
  key_prefix: "onr-prod"
  billing_stream: "onr:billing-events"
  billing_consumer_group: "onr-billing"
  access_key_hash_secret: "${ONR_ACCESS_KEY_HASH_SECRET}"
```

Redis 是当前部署的权威存储，生产环境必须启用持久化、认证、备份和高可用。
明文 Access Key 只在创建或轮换时返回一次，不写入 Redis。

初始额度按照账户幂等发放一次，轮换 Key 不会重复发放。金额按配置货币的百万分之一
整数保存，最多支持六位小数。

请求成功返回后，ONR 根据供应商 DSL 提取的 Token 用量和本地价格配置计算费用，按
请求 ID 写入一条不可变事件；重复请求事件不会重复计费。用户报表按模型聚合，不暴露
供应商和内部 Key。

ONR 不做请求前额度预占。余额较低时不硬拦截，并发请求允许产生小幅临时超额，最终以
供应商返回的实际用量计费。

管理员可以创建、查询、轮换、撤销 Access Key，以及增加或扣减额度。调整额度必须提供
幂等 Key。用户使用 Access Key 登录用户页面，查看余额、请求明细、Token、金额，以及
按小时、天、周和自定义时间段统计的模型用量。
分布式部署要求至少两个 ONR 实例、负载均衡和同一个高可用 Redis。所有实例必须使用
相同的 Key 前缀、哈希密钥、DSL、模型路由、价格文件和供应商密钥。Redis 不可用时认证
和账本操作失败关闭。进程在事件写入 Redis 前崩溃，可能留下计费缺口，应通过日志和供应
商请求记录监控与补偿。
