# ONR 生产环境部署

本文用于在新的 Linux 服务器上部署 open-next-router（ONR），包括 Redis
访问密钥管理、额度计费、OpenAI 兼容中转接口，以及管理员和用户用量网站。
本文假设服务器上已经运行 `docs/START_OTHER.md` 描述的其他服务。

已有服务继续使用原来的 Nginx `80/443` 端口和应用端口。ONR 只监听本机
`3300`，管理员和用户网站只监听本机 `3310`。公网访问统一通过现有 Nginx
按域名转发，不要启动第二个 Nginx。

```text
OpenAI 客户端 -> https://api.example.com   -> Nginx -> 127.0.0.1:3300（ONR）
浏览器       -> https://meter.example.com -> Nginx -> 127.0.0.1:3310（网站）
                                                  |
                                                  +-> Redis（现有 6379，DB 1）
                                                  +-> 电信 Ctyun API
```

请先替换以下占位符：

| 占位符 | 含义 |
| --- | --- |
| `api.example.com` | 模型 API 公网域名 |
| `meter.example.com` | 管理员和用户用量网站域名 |
| `/home/open-next-router` | ONR 安装目录 |
| `root` | ONR 运行用户，本部署方案使用 root |

## 1. 检查现有服务和 DNS

使用 root 用户执行以下步骤。本文按你的要求让 ONR 和网站以 root 运行；这会
降低进程隔离和主机安全性，只适合受控测试环境或你明确接受该风险的服务器。
公网生产环境更建议创建专用用户，并限制服务权限。

确认当前 shell 确实是 root：

```bash
id -u
```

输出必须为 `0`。以下命令均按 root 执行，因此不再添加 `sudo` 前缀。

```bash
ss -ltnp
systemctl status nginx --no-pager
systemctl status redis-server --no-pager || systemctl status redis --no-pager
```

确认 `START_OTHER.md` 中的现有服务仍然正常运行。不要停止或覆盖其
systemd、Nginx、PostgreSQL 和 Redis 配置。

为两个域名创建指向服务器公网 IP 的 DNS `A`/`AAAA` 记录：

```text
api.example.com   -> SERVER_PUBLIC_IP
meter.example.com -> SERVER_PUBLIC_IP
```

## 2. 安装系统依赖

推荐 Ubuntu 22.04/24.04 或兼容的 Debian 系统：

```bash
apt update
apt install -y git curl ca-certificates build-essential nginx redis-server ufw
systemctl enable --now nginx
```

ONR 要求使用仓库 `go.mod` 声明的 Go 版本：

```bash
grep '^go ' go.mod
```

例如当前仓库使用 Go 1.26.6，AMD64 服务器执行：

```bash
cd /tmp
curl -fLO https://go.dev/dl/go1.26.6.linux-amd64.tar.gz
rm -rf /usr/local/go
tar -C /usr/local -xzf go1.26.6.linux-amd64.tar.gz
export PATH=/usr/local/go/bin:$PATH
go version
```

ARM64 服务器应下载对应的 `linux-arm64` 压缩包，并将 PATH 写入登录配置：

```bash
echo 'export PATH=/usr/local/go/bin:$PATH' >> ~/.profile
```

如果 `START_OTHER.md` 中的服务已经使用 Redis `6379`，直接复用，不要再
启动第二个 Redis：

```bash
redis-cli -h 127.0.0.1 -p 6379 ping
```

ONR 使用 Redis DB `1` 和独立的 `onr-prod` key prefix，避免与其他服务冲突。
如果 Redis 启用了认证，在 ONR 配置中填写对应用户名和密码，不要盲目修改
已有 Redis 配置。

## 3. 下载源码并编译

```bash
install -d -o root -g root /home/open-next-router
git clone --recurse-submodules \
  https://github.com/r9s-ai/open-next-router.git \
  /home/open-next-router
cd /home/open-next-router
git submodule update --init --recursive
```

如果目录已有经过审核的代码，则按发布流程更新，不要覆盖配置目录。编译
ONR 和管理员网站两个二进制文件：

```bash
export PATH=/usr/local/go/bin:$PATH
GOCACHE=/tmp/onr-go-build-cache GOMODCACHE=/tmp/onr-go-mod-cache go mod download
make build
install -o root -g root -m 0755 bin/onr /usr/local/bin/onr
install -o root -g root -m 0755 bin/onr-admin /usr/local/bin/onr-admin
```

## 4. 创建运行配置

```bash
install -d -o root -g root -m 0750 /etc/onr
install -d -o root -g root -m 0750 /var/lib/onr/run/oauth
cp config/onr.example.yaml /etc/onr/onr.yaml
cp config/keys.example.yaml /etc/onr/keys.yaml
cp config/models.example.yaml /etc/onr/models.yaml
cp config/price.public.yaml /etc/onr/price.public.yaml
chown root:root /etc/onr/*.yaml
chmod 0600 /etc/onr/keys.yaml
```

编辑 `/etc/onr/onr.yaml`，使用绝对路径，并至少设置以下内容：

```yaml
server:
  listen: "127.0.0.1:3300"
  # Upstream and response timeout: 10 minutes for complex generations.
  read_timeout_ms: 600000
  write_timeout_ms: 600000
  pid_file: "/run/onr/onr.pid"
keys:
  file: "/etc/onr/keys.yaml"
models:
  file: "/etc/onr/models.yaml"
  catalog_file: "/home/open-next-router/config/models.catalog.yaml"
pricing:
  enabled: true
  file: "/etc/onr/price.public.yaml"
billing:
  enabled: true
  currency: "CNY"
  initial_credit: "200"
redis:
  enabled: true
  addr: "redis://127.0.0.1:6379/1"
  username: ""
  password: ""
  tls: false
  key_prefix: "onr-prod"
  access_key_mode: "redis_preferred"
  billing_stream: "onr-prod:billing-events"
  billing_consumer_group: "onr-billing"
  access_key_hash_secret: "GENERATE_A_LONG_RANDOM_SECRET"
```

实际字段名以当前仓库的 `config/onr.example.yaml` 为准。使用下面的命令
生成 `access_key_hash_secret`，并将结果填入配置。该值必须长期保持不变，
否则已有 Access Key 将无法匹配。

```bash
openssl rand -hex 32
vi /etc/onr/onr.yaml
```

把电信 Ctyun 的内部 Key 写入 `/etc/onr/keys.yaml`，不要写入公开 Git 目录：

```yaml
providers:
  ctyun:
    keys:
      - name: "ctyun-primary"
        value: "REPLACE_WITH_CTYUN_KEY"
```

实际字段以 `reference/ctyun` 文档和当前 `config/providers/ctyun.conf` 为准。
完成后锁定文件权限：

```bash
chown root:root /etc/onr/keys.yaml
chmod 0600 /etc/onr/keys.yaml
```

`/etc/onr/models.yaml` 应包含公开模型 ID，并在已支持的模型上配置 `ctyun`。
供应商模型映射必须继续放在显式 DSL 配置中，不能在运行时代码中增加路径、
API 或模型猜测规则。

## 5. 本地验证

先测试配置：

```bash
/usr/local/bin/onr -t -c /etc/onr/onr.yaml
```

临时以前台方式启动：

```bash
/usr/local/bin/onr --config /etc/onr/onr.yaml
```

在另一个终端测试网关。这里使用已配置的测试 Key；正式 Access Key 应在
Redis 正常后通过管理员网站创建：

```bash
curl -fsS http://127.0.0.1:3300/v1/models \
  -H 'Authorization: Bearer MASTER_OR_TEST_KEY'
```

验证结束后用 `Ctrl-C` 停止前台进程。

## 6. 使用 systemd 部署

创建 `/etc/systemd/system/onr.service`：

```ini
[Unit]
Description=open-next-router gateway
After=network-online.target redis-server.service
Wants=network-online.target

[Service]
User=root
Group=root
WorkingDirectory=/home/open-next-router
RuntimeDirectory=onr
ExecStart=/usr/local/bin/onr --config /etc/onr/onr.yaml
Restart=always
RestartSec=3
LimitNOFILE=65536

[Install]
WantedBy=multi-user.target
```

创建 `/etc/systemd/system/onr-admin-web.service`：

```ini
[Unit]
Description=ONR admin and user web portal
After=network-online.target redis-server.service onr.service
Wants=network-online.target

[Service]
User=root
Group=root
WorkingDirectory=/home/open-next-router
Environment=ONR_ADMIN_WEB_TOKEN=REPLACE_WITH_ADMIN_WEB_TOKEN
Environment=ONR_ADMIN_WEB_CURL_API_BASE_URL=https://api.example.com
ExecStart=/usr/local/bin/onr-admin web --config /etc/onr/onr.yaml --listen 127.0.0.1:3310
Restart=always
RestartSec=3
LimitNOFILE=16384

[Install]
WantedBy=multi-user.target
```

生成强随机管理员 Token，替换 unit 中的占位符，然后启动服务：

```bash
openssl rand -hex 32
systemctl daemon-reload
systemd-analyze verify /etc/systemd/system/onr.service /etc/systemd/system/onr-admin-web.service
systemctl enable --now onr.service onr-admin-web.service
systemctl status onr.service onr-admin-web.service --no-pager
```

检查两个本机监听端口：

```bash
curl -fsS http://127.0.0.1:3300/v1/models -H 'Authorization: Bearer MASTER_OR_TEST_KEY'
curl -fsSI http://127.0.0.1:3310/
journalctl -u onr -u onr-admin-web -n 100 --no-pager
```

## 7. 创建 Access Key 并验证计费

管理员网站和用户用量网站由同一个 `onr-admin web` 服务提供。部署初期可
通过 SSH 隧道访问：

```bash
ssh -N -L 3310:127.0.0.1:3310 DEPLOY_USER@SERVER_PUBLIC_IP
```

打开 `http://127.0.0.1:3310`，使用管理员 Token 登录并创建 Access Key，设置
初始 CNY 额度。Access Key 不限制模型，管理员负责切换供应商。

首次测试 Ctyun：

1. 在管理员总览中确认 Redis 状态正常。
2. 创建 Access Key，并保存创建时显示的 Secret。
3. 为该 Access Key 选择或切换到 Ctyun。
4. 使用 Access Key 调用公网 Chat Completions 接口。
5. 使用同一个 Access Key 登录用户网站，查看余额、模型用量、请求明细和趋势图。

```bash
curl https://api.example.com/v1/chat/completions \
  -H 'Authorization: Bearer ak_REPLACE_WITH_ACCESS_KEY' \
  -H 'Content-Type: application/json' \
  -d '{"model":"qwen3.8-max","messages":[{"role":"user","content":"你好"}],"max_tokens":128}'
```

ONR 在模型响应完成后记录本次用量，不会在请求前预占全部额度。余额很少时，
允许少量超额。

## 8. 配置 Nginx 公网转发

使用新的 Nginx site 配置，不要启动第二个 Nginx，也不要覆盖现有服务的
server block。保存为 `/etc/nginx/sites-available/onr`：

```nginx
server {
    listen 80;
    listen [::]:80;
    server_name api.example.com;
    location / {
        proxy_pass http://127.0.0.1:3300;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_buffering off;
        proxy_read_timeout 900s;
    }
}

server {
    listen 80;
    listen [::]:80;
    server_name meter.example.com;
    location / {
        proxy_pass http://127.0.0.1:3310;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

启用并检查配置，然后重载现有 Nginx：

```bash
ln -s /etc/nginx/sites-available/onr /etc/nginx/sites-enabled/onr
nginx -t
systemctl reload nginx
```

如果两个域名已经被 `START_OTHER.md` 的服务使用，请更换域名，不要创建重复
的 `server_name`。公网不应直接开放 ONR 的 `3300` 或网站的 `3310`。

## 9. HTTPS 和防火墙

确认两个域名已经解析后申请证书：

```bash
apt install -y certbot python3-certbot-nginx
certbot --nginx -d api.example.com -d meter.example.com
certbot renew --dry-run
```

只允许 SSH、HTTP 和 HTTPS 端口对公网开放：

```bash
ufw allow OpenSSH
ufw allow 80/tcp
ufw allow 443/tcp
ufw enable
ufw status verbose
```

## 10. 运维和故障排查

常用命令：

```bash
systemctl restart onr onr-admin-web
journalctl -fu onr
journalctl -fu onr-admin-web
systemctl reload onr
/usr/local/bin/onr -t -c /etc/onr/onr.yaml
```

### `address already in use`

执行 `ss -ltnp` 查找占用端口。不要停止已有服务，只修改 ONR 的本机
端口和对应的 Nginx `proxy_pass`。

### `redis access-key management is disabled`

检查 Redis 连接、DB、key prefix、`redis.enabled: true` 以及非空且稳定的
`access_key_hash_secret`，然后重启 ONR 和网站服务。

### `401 invalid_api_key`

使用 Access Key Secret，不是管理页面显示名称。确认请求访问的是同一个 ONR
实例，并且 Redis DB、key prefix 和 hash secret 没有变化。

### 模型不可用

检查 `/etc/onr/models.yaml`、模型目录和
`config/providers/ctyun.conf` 中的显式模型映射。模型可以出现在目录中，
但没有供应商映射时仍不能路由。

### 用量没有显示

检查 `pricing.enabled`、`billing.enabled`、Redis 健康状态，以及管理员总览中
是否存在待处理或死信计费事件：

```bash
journalctl -u onr -u onr-admin-web --since '15 minutes ago' --no-pager
```

备份 `/etc/onr/keys.yaml`、`/etc/onr/onr.yaml` 和 ONR 使用的 Redis DB。不要把
这些文件提交到公开仓库或打印到日志。每次升级后都应重新执行配置测试，并
验证一次模型请求和一条用户用量记录。

## 11. 更新 ONR 代码

以下步骤适用于本文的 root 部署方式。代码更新不会自动覆盖
`/etc/onr/onr.yaml`、`/etc/onr/keys.yaml`、`/etc/onr/models.yaml` 或价格配置。
更新前请先确认当前服务器配置和 Redis 数据已经备份。

### 11.1 更新前检查和备份

```bash
cd /home/open-next-router
id -u
git status --short
systemctl status onr onr-admin-web --no-pager
mkdir -p /root/onr-backups/$(date +%Y%m%d-%H%M%S)
BACKUP_DIR=$(ls -td /root/onr-backups/* | head -1)
cp -a /etc/onr "$BACKUP_DIR/"
cp -a /usr/local/bin/onr /usr/local/bin/onr-admin "$BACKUP_DIR/"
```

如果 `git status --short` 显示服务器上的本地修改，先保存这些修改并确认
它们是否应该保留。不要在没有确认的情况下使用 `git reset --hard`，因为这
会删除本地修改。

### 11.2 拉取新版本并编译

切换到经过审核的分支或版本，然后以 fast-forward 方式更新：

```bash
cd /home/open-next-router
git fetch origin
git pull --ff-only origin main
git submodule update --init --recursive
```

如果部署的是固定版本，使用明确的 tag 或 commit：

```bash
git fetch --tags origin
git checkout vX.Y.Z
git submodule update --init --recursive
```

编译新二进制文件。这里使用独立的 Go 缓存，不影响正在运行的 ONR：

```bash
export PATH=/usr/local/go/bin:$PATH
GOCACHE=/tmp/onr-go-build-cache GOMODCACHE=/tmp/onr-go-mod-cache go mod download
make build
```

### 11.3 更新前验证

先验证新代码和当前生产配置：

```bash
/home/open-next-router/bin/onr -t -c /etc/onr/onr.yaml
/home/open-next-router/bin/onr --help >/dev/null
/home/open-next-router/bin/onr-admin --help >/dev/null
```

如果本次更新修改了 `config/providers/*.conf`、模型目录或计费相关代码，建议
先执行测试：

```bash
GOCACHE=/tmp/onr-go-build-cache GOMODCACHE=/tmp/onr-go-mod-cache go test ./...
```

测试或配置校验失败时不要替换线上二进制，先查看错误并修复。

### 11.4 原子替换并重启

确认验证通过后，用临时文件替换两个二进制，避免服务读取到不完整文件：

```bash
install -o root -g root -m 0755 /home/open-next-router/bin/onr /usr/local/bin/onr.new
install -o root -g root -m 0755 /home/open-next-router/bin/onr-admin /usr/local/bin/onr-admin.new
mv -f /usr/local/bin/onr.new /usr/local/bin/onr
mv -f /usr/local/bin/onr-admin.new /usr/local/bin/onr-admin
systemctl daemon-reload
systemctl restart onr.service onr-admin-web.service
systemctl status onr.service onr-admin-web.service --no-pager
```

通常不需要重启 Redis，也不要因为 ONR 更新而删除 Redis DB、key prefix 或
计费 Stream。这样可以保留 Access Key、余额、请求记录和计费事件。

### 11.5 更新后验证

先检查服务日志和本机接口：

```bash
journalctl -u onr -u onr-admin-web -n 100 --no-pager
curl -fsS http://127.0.0.1:3300/v1/models \
  -H 'Authorization: Bearer MASTER_OR_TEST_KEY'
curl -fsSI http://127.0.0.1:3310/
```

然后通过公网域名验证：

```bash
curl -fsS https://api.example.com/v1/models \
  -H 'Authorization: Bearer ak_REPLACE_WITH_ACCESS_KEY'
```

使用一个可调用的 Ctyun 模型发送最小 Chat Completions 请求，确认响应正常；
再登录 `https://meter.example.com/`，确认余额、请求明细和用量趋势仍然可见。

如果更新包含 DSL、模型映射或计费逻辑变化，还应检查管理员页面中的 Redis
状态、待处理计费事件和死信数量。

### 11.6 更新失败时回滚

如果新版本启动失败或接口异常，先停止服务，再恢复备份的二进制。将下面的
`BACKUP_DIR` 替换为更新前实际创建的备份目录：

```bash
systemctl stop onr-admin-web.service onr.service
install -o root -g root -m 0755 "$BACKUP_DIR/onr" /usr/local/bin/onr
install -o root -g root -m 0755 "$BACKUP_DIR/onr-admin" /usr/local/bin/onr-admin
systemctl start onr.service onr-admin-web.service
systemctl status onr.service onr-admin-web.service --no-pager
```

如果新版本修改了配置格式，不要直接恢复旧配置覆盖当前文件。先查看日志，
根据版本说明完成必要的配置迁移。除非明确需要数据迁移，否则不要删除或
更换 Redis DB、`key_prefix`、`billing_stream` 或 `access_key_hash_secret`。
