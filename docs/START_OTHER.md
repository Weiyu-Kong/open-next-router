# ARC-Bench 新 Linux 服务器部署手册

本文用于在一台全新的 Ubuntu 22.04/24.04（或兼容 Debian 系统）上部署当前版本 ARC-Bench。当前推荐架构是同机部署：FastAPI、PostgreSQL、Redis、Celery Worker、Outbox Dispatcher、租约恢复器和 Docker Runner。

```text
浏览器 -> Nginx -> Gunicorn/FastAPI -> PostgreSQL
                                \-> Redis -> Celery Worker -> Docker Runner
```

生产环境必须使用 PostgreSQL；没有 `ARCBENCH_DATABASE_URL` 或 `config.yaml` 中的 `database_url` 时，应用会拒绝启动。请准备一个 DNS 名称、开放的 HTTP/HTTPS 端口，以及模型服务的 API 凭据。

## 1. 系统初始化

使用有 sudo 权限的部署用户登录服务器。不要用 root 运行应用、Celery 或 Docker Runner。

```bash
sudo apt update
sudo apt upgrade -y
sudo apt install -y \
  git curl ca-certificates build-essential pkg-config libssl-dev \
  python3 python3-venv python3-pip python3-dev \
  postgresql postgresql-contrib postgresql-client \
  redis-server nginx docker.io
sudo systemctl enable --now postgresql redis-server docker
sudo usermod -aG docker "$USER"
```

执行 `usermod` 后必须退出 SSH 并重新登录（或执行 `newgrp docker`）。确认 Docker 可用：

```bash
docker version
docker run --rm hello-world
```

安装 Node.js 20（前端构建需要）：

```bash
curl -fsSL https://deb.nodesource.com/setup_20.x | sudo -E bash -
sudo apt install -y nodejs
node --version
npm --version
```

## 2. 下载代码和必需的外部仓库

本文当前服务器使用 `/home/arc-bench-website` 作为项目目录。若更换目录，之后所有 systemd 路径必须保持一致。

```bash
sudo mkdir -p /home/arc-bench-website
sudo chown -R "$USER":"$USER" /home/arc-bench-website
cd /home/arc-bench-website
git clone <ARC_BENCH_REPOSITORY_URL> .

# 这两个目录是独立 Git 工作副本，不会由主仓库自动生成。
git clone https://github.com/Weiyu-Kong/arc-template.git arc-template
git clone --recurse-submodules \
  https://github.com/code-philia/agentic-requirement-compiler.git \
  reference-implementations/arc
git -C reference-implementations/arc submodule update --init --recursive
```

确认以下目录存在：`backend/`、`frontend/`、`data/playground/`、`arc-template/`、`reference-implementations/arc/` 和 `runtime/demo-agent/`。Quick Start 固定回放依赖 `runtime/demo-agent/template.git.bundle`，不要删除该文件。

## 3. 创建 PostgreSQL 数据库

生成随机密码并创建应用账号。密码不要提交 Git，也不要写入公开脚本。

```bash
sudo -u postgres psql
```

在 `psql` 中执行：

```sql
CREATE ROLE arcbench LOGIN PASSWORD 'REPLACE_WITH_LONG_RANDOM_PASSWORD';
CREATE DATABASE arcbench OWNER arcbench ENCODING 'UTF8' TEMPLATE template0;
ALTER DATABASE arcbench SET timezone TO 'UTC';
REVOKE ALL ON DATABASE arcbench FROM PUBLIC;
GRANT CONNECT, TEMPORARY ON DATABASE arcbench TO arcbench;
\q
```

同机部署可先使用 `sslmode=prefer`；跨主机或公网连接应配置 TLS，并使用 `sslmode=verify-full`。

## 4. 配置密钥和环境变量

复制示例文件，设置严格权限：

```bash
cd /home/arc-bench-website
cp config.example.yaml config.yaml
cp source.example.sh source.sh
chmod 600 config.yaml source.sh
${EDITOR:-vi} config.yaml
${EDITOR:-vi} source.sh
```

至少填写以下内容：

```bash
export ARCBENCH_DATABASE_URL='postgresql+psycopg://arcbench:URL_ENCODED_PASSWORD@127.0.0.1:5432/arcbench?sslmode=prefer'
export ARCBENCH_REDIS_URL='redis://:URL_ENCODED_REDIS_PASSWORD@127.0.0.1:6379/0'
export ARCBENCH_SESSION_SECRET='至少32字节的随机字符串'
export ARCBENCH_RUNNER_IMAGE='arcbench-runner:prod-20260902'
export ARCBENCH_RUNNER_BUILD_ON_DEMAND='false'
# source.sh is sourced by an interactive shell, so this is also valid there.
# For the current root setup use literal UID:GID 0:0.
export ARCBENCH_RUNNER_USER="0:0"
export ARCBENCH_MAX_CONCURRENT_RUNS='4'
```

如果暂时让 Redis default 用户无密码，URL 可写为 `redis://127.0.0.1:6379/0`；生产环境建议启用 ACL 和密码。所有包含特殊字符的密码必须进行 URL 编码。加载并检查配置（不会显示密码）：

```bash
source ./source.sh
backend/.venv/bin/python -c 'from app.core.config import get_settings; from sqlalchemy.engine import make_url; s=get_settings(); print(make_url(s.database_url).render_as_string(hide_password=True)); print(make_url(s.redis_url).render_as_string(hide_password=True))'
```

## 5. 安装 Python 和前端依赖

```bash
cd /home/arc-bench-website
python3 -m venv backend/.venv
backend/.venv/bin/python -m pip install --upgrade pip wheel
backend/.venv/bin/python -m pip install -r backend/requirements.txt

cd frontend
npm ci
npm run build
cd ..
```

验证关键依赖：

```bash
backend/.venv/bin/python -c 'import fastapi, sqlalchemy, celery, redis, psycopg; print("python dependencies: OK")'
```

## 6. 初始化数据库和检查基础服务

```bash
source ./source.sh
backend/.venv/bin/alembic -c alembic.ini upgrade head
backend/.venv/bin/python scripts/check_postgresql.py
backend/.venv/bin/python scripts/check_redis.py
```

若数据库来自旧 SQLite 安装，应先按 [PostgreSQL.md](PostgreSQL.md) 完成备份和迁移，再执行 Alembic。不要在生产库中直接运行 `Base.metadata.create_all()` 或手工修改表结构。

## 7. 构建并验证 Docker Runner

Runner 镜像必须在接受任务前构建完成。生产环境关闭按需构建，使用不可变版本标签：

```bash
source ./source.sh
docker build -f backend/runner/Dockerfile -t "$ARCBENCH_RUNNER_IMAGE" .
docker run --rm --entrypoint python3 "$ARCBENCH_RUNNER_IMAGE" /opt/arcbench/smoke_test.py
docker image inspect "$ARCBENCH_RUNNER_IMAGE" >/dev/null
```

如果修改了 `backend/runner/Dockerfile` 或 `run_submission.py`，必须重新构建并更新 `ARCBENCH_RUNNER_IMAGE`。Runner 会使用 `ARCBENCH_RUNNER_USER` 写入宿主机工作区，避免 root 文件导致 Celery 无法写日志。

## 8. 首次手工启动和验收

先在四个终端分别启动服务，确认功能正常后再交给 systemd。

终端 1（API）：

```bash
cd /home/arc-bench-website
source ./source.sh
cd backend
.venv/bin/gunicorn app.main:app -k uvicorn.workers.UvicornWorker \
  --workers 2 --bind 127.0.0.1:8000 --timeout 120 --keep-alive 5 --access-logfile -
```

终端 2（Outbox Dispatcher）：

```bash
cd /home/arc-bench-website
source ./source.sh
backend/.venv/bin/python -m app.worker.dispatcher
```

终端 3（Celery Worker）：

```bash
cd /home/arc-bench-website
source ./source.sh
cd backend
.venv/bin/celery -A app.worker.celery_app worker \
  --loglevel=INFO --concurrency=4 -Q evaluation.default,evaluation.retry
```

终端 4（过期租约恢复器）：

```bash
cd /home/arc-bench-website
source ./source.sh
backend/.venv/bin/python -m app.worker.recovery
```

验收命令：

```bash
curl -fsS http://127.0.0.1:8000/api/health
cd backend
.venv/bin/celery -A app.worker.celery_app inspect ping
.venv/bin/celery -A app.worker.celery_app inspect registered
```

登录网页后，从首页 Quick Start 创建一次固定 Demo，确认 Run 从 `PENDING/QUEUED` 进入 `RUNNING` 并最终完成；再提交一个普通 ZIP Agent，确认两条流程都可用。检查 `runtime/user-submissions/` 下的工作区和日志权限应属于部署用户。

## 9. 使用 systemd 托管生产服务

创建统一环境文件。`source.sh` 是 shell 脚本，不能直接作为 systemd 的
`EnvironmentFile`，因为其中的 `export` 和命令替换语法会被 systemd 忽略。只提取
`export KEY=value` 行：

```bash
grep '^export ' source.sh | sed 's/^export //' | sudo tee /etc/arcbench.env >/dev/null
sudo chmod 600 /etc/arcbench.env
```

检查文件中每行都形如 `KEY=value`（不能有 `export`）：

```bash
sudo sed -n '1,12p' /etc/arcbench.env
sudo systemd-analyze verify /etc/systemd/system/arcbench-api.service
```

修改 `source.sh` 后必须重新生成环境文件并重启相关服务：

```bash
grep '^export ' source.sh | sed 's/^export //' | sudo tee /etc/arcbench.env >/dev/null
sudo chmod 600 /etc/arcbench.env
sudo systemctl daemon-reload
sudo systemctl restart arcbench-api arcbench-worker arcbench-dispatcher arcbench-recovery
```

创建四个 unit。根据你当前环境，以下示例使用 `root` 和 `/home/arc-bench-website`。**不建议长期让 API 和 Runner 以 root 运行**：Runner 执行用户提交的代码，root 权限可能导致宿主机被完全控制。迁移到专用用户时，请同时替换所有 `User`、`Group` 和路径。

```bash
sudo tee /etc/systemd/system/arcbench-api.service >/dev/null <<'EOF'
[Unit]
After=network-online.target postgresql.service redis-server.service docker.service
Wants=network-online.target
[Service]
User=root
Group=root
WorkingDirectory=/home/arc-bench-website/backend
EnvironmentFile=/etc/arcbench.env
ExecStart=/home/arc-bench-website/backend/.venv/bin/gunicorn app.main:app -k uvicorn.workers.UvicornWorker --workers 2 --bind 127.0.0.1:8000 --timeout 120 --keep-alive 5 --access-logfile -
Restart=always
[Install]
WantedBy=multi-user.target
EOF

sudo tee /etc/systemd/system/arcbench-worker.service >/dev/null <<'EOF'
[Unit]
After=network-online.target postgresql.service redis-server.service docker.service
[Service]
User=root
Group=root
WorkingDirectory=/home/arc-bench-website/backend
EnvironmentFile=/etc/arcbench.env
ExecStart=/home/arc-bench-website/backend/.venv/bin/celery -A app.worker.celery_app worker --loglevel=INFO --concurrency=4 -Q evaluation.default,evaluation.retry
Restart=always
[Install]
WantedBy=multi-user.target
EOF

sudo tee /etc/systemd/system/arcbench-dispatcher.service >/dev/null <<'EOF'
[Unit]
After=postgresql.service redis-server.service
[Service]
User=root
Group=root
WorkingDirectory=/home/arc-bench-website/backend
EnvironmentFile=/etc/arcbench.env
ExecStart=/home/arc-bench-website/backend/.venv/bin/python -m app.worker.dispatcher
Restart=always
[Install]
WantedBy=multi-user.target
EOF

sudo tee /etc/systemd/system/arcbench-recovery.service >/dev/null <<'EOF'
[Unit]
After=postgresql.service redis-server.service
[Service]
User=root
Group=root
WorkingDirectory=/home/arc-bench-website/backend
EnvironmentFile=/etc/arcbench.env
ExecStart=/home/arc-bench-website/backend/.venv/bin/python -m app.worker.recovery
Restart=always
[Install]
WantedBy=multi-user.target
EOF

sudo systemctl daemon-reload
sudo systemctl enable --now arcbench-api arcbench-worker arcbench-dispatcher arcbench-recovery
sudo systemctl status arcbench-api arcbench-worker arcbench-dispatcher arcbench-recovery --no-pager
```

当前示例已经使用 `root`。如果改用专用部署用户，必须把四个 unit 中的 `User=root` 和 `Group=root` 改为实际用户。先查询 UID/GID：

```bash
id -u <部署用户名>
id -g <部署用户名>
```

然后在 `/etc/arcbench.env` 中写入实际数字，例如 `ARCBENCH_RUNNER_USER=1000:1000`。systemd 的 `EnvironmentFile` 不会执行 `$(id -u)` 命令替换。root 默认可访问 Docker；若改用非 root 用户，确认该用户属于 Docker 组：

```bash
id <部署用户名>
getent group docker
```

查看日志：

```bash
journalctl -u arcbench-api -u arcbench-worker -u arcbench-dispatcher -u arcbench-recovery -f
```

## 10. Nginx 和 HTTPS

Gunicorn 只监听 `127.0.0.1:8000`，公网访问必须通过 Nginx。先创建站点配置：

```nginx
server {
    listen 80;
    server_name example.com;
    location / {
        proxy_pass http://127.0.0.1:8000;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_buffering off;
        proxy_read_timeout 3600s;
    }
}
```

把 `example.com` 换成真实域名；如果暂时没有域名，可以把 `server_name` 改为服务器公网 IP（HTTPS 证书仍建议使用域名）：

```bash
sudo nano /etc/nginx/sites-available/arcbench
sudo ln -s /etc/nginx/sites-available/arcbench /etc/nginx/sites-enabled/arcbench
sudo rm -f /etc/nginx/sites-enabled/default
sudo nginx -t
sudo systemctl reload nginx
```

配置主机防火墙，只开放 SSH、HTTP 和 HTTPS，不要开放 8000、5432、6379 或 Docker API：

```bash
sudo ufw allow OpenSSH
sudo ufw allow 80/tcp
sudo ufw allow 443/tcp
sudo ufw --force enable
sudo ufw status verbose
```

同时在云厂商安全组/防火墙中放行 TCP 80 和 443，来源可先设为 `0.0.0.0/0`；SSH 端口应限制为管理员 IP。PostgreSQL、Redis 只允许本机或私网地址访问。

启用 HTTPS：

```bash
sudo apt install -y certbot python3-certbot-nginx
sudo certbot --nginx -d example.com
sudo systemctl status certbot.timer --no-pager
```

证书配置完成后，把 `source.sh` 和 `/etc/arcbench.env` 中的来源改为真实 HTTPS 地址：

```bash
export ARCBENCH_SECURE_COOKIES='true'
export ARCBENCH_CORS_ORIGINS='["https://example.com"]'
```

然后重启 API：

```bash
sudo systemctl restart arcbench-api
```

从另一台不在服务器上的电脑验收公网访问：

```bash
curl -I https://example.com/
curl -fsS https://example.com/api/health
```

浏览器访问 `https://example.com/`。若本机 `curl http://127.0.0.1:8000/api/health` 正常但公网失败，按顺序检查：DNS 是否指向正确公网 IP、云安全组是否放行 80/443、`nginx -t` 是否通过、以及 `journalctl -u nginx`。

## 10.1 SSH 断开后的行为

不要在 SSH 终端中直接运行 Gunicorn、Celery 或 Dispatcher 作为生产进程；关闭终端会发送 SIGHUP，进程可能退出。systemd 托管后服务与 SSH 会话无关：

```bash
sudo systemctl is-enabled arcbench-api arcbench-worker arcbench-dispatcher arcbench-recovery
sudo systemctl is-active arcbench-api arcbench-worker arcbench-dispatcher arcbench-recovery
```

四个服务都应分别输出 `enabled` 和 `active`。现在可以安全退出 SSH：

```bash
exit
```

重新连接后，检查服务仍在运行：

```bash
sudo systemctl status arcbench-api arcbench-worker arcbench-dispatcher arcbench-recovery --no-pager
```

服务器重启后，`enable --now` 已配置的服务会自动启动；若服务异常退出，unit 中的 `Restart=always` 会自动重启。实时查看原因：

```bash
sudo journalctl -u arcbench-api -u arcbench-worker -u arcbench-dispatcher -u arcbench-recovery -f
```

## 11. 部署完成检查表

- [ ] `systemctl is-active postgresql redis-server docker` 全部为 `active`。
- [ ] `check_postgresql.py` 和 `check_redis.py` 通过。
- [ ] `alembic upgrade head` 成功，数据库为 PostgreSQL。
- [ ] Runner smoke test 通过，镜像标签与 `ARCBENCH_RUNNER_IMAGE` 一致。
- [ ] Celery `inspect ping` 能发现 Worker。
- [ ] Dispatcher 和 recovery 服务均为 `active`。
- [ ] Quick Start 固定 Demo 能完成一次回放。
- [ ] 普通上传 Agent 能创建并运行。
- [ ] 任务失败时日志仍可写入，工作区文件不属于 root。
- [ ] 已配置 PostgreSQL、Redis、配置文件和运行目录备份。

更新代码时，先停止 API/Worker，执行 `git pull`、重新安装依赖、重新构建前端和 Runner、执行 `alembic upgrade head`，最后 `systemctl restart arcbench-api arcbench-worker arcbench-dispatcher arcbench-recovery`。不要在运行中的生产任务上直接替换工作区或删除 Redis 数据。
