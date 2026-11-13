# ti-temp-mail-api

基于 Go 实现的自托管临时邮箱服务。内置 SMTP 服务器接收 catch-all 邮件，通过 HTTP API 创建邮箱并读取邮件。默认纯内存运行，也支持 SQLite（无 CGO）、MySQL、PostgreSQL 以及 Cloudflare D1。

---

## 快速启动

### 1. 默认内存模式（最简单，重启后清空）

```bash
docker run -d \
  --name ti-temp-mail-api \
  --restart unless-stopped \
  -e MAIL_DOMAIN=example.com \
  -p 8080:8080 \
  -p 25:25 \
  ghcr.io/jbtt-2025/ti-temp-mail-api:latest
```

### 2. SQLite 持久化模式（Docker 目录挂载）

> [!IMPORTANT]
> 容器工作目录为 `/app`，SQLite 默认启用 WAL 模式，会产生 `*.db`、`*.db-wal`、`*.db-shm` 三个同级文件。
> **请挂载目录（推荐 `-v ./data:/app/data`），不要直接挂载单个 `.db` 文件**，否则宿主机文件不存在时会被 Docker 建为目录，且 WAL 辅助文件无法持久化。

```bash
mkdir -p ./data

docker run -d \
  --name ti-temp-mail-api \
  --restart unless-stopped \
  -e MAIL_DOMAIN=example.com \
  -e STORAGE_TYPE=sqlite \
  -e DB_DSN=/app/data/temp-mail.db \
  -e MAILBOX_TTL=24h \
  -e EMAIL_TTL=24h \
  -e CLEANUP_INTERVAL=5m \
  -v "$(pwd)/data:/app/data" \
  -p 8080:8080 \
  -p 25:25 \
  ghcr.io/jbtt-2025/ti-temp-mail-api:latest
```

或使用 `docker-compose.yml`：

```yaml
services:
  temp-mail:
    image: ghcr.io/jbtt-2025/ti-temp-mail-api:latest
    restart: unless-stopped
    ports:
      - "8080:8080"
      - "25:25"
    environment:
      MAIL_DOMAIN: example.com
      STORAGE_TYPE: sqlite
      DB_DSN: /app/data/temp-mail.db
      MAILBOX_TTL: 24h
      EMAIL_TTL: 24h
      CLEANUP_INTERVAL: 5m
    volumes:
      - ./data:/app/data
```

### 3. MySQL / PostgreSQL 模式

**MySQL：**

```bash
docker run -d \
  --name ti-temp-mail-api \
  --restart unless-stopped \
  -e MAIL_DOMAIN=example.com \
  -e STORAGE_TYPE=mysql \
  -e DB_DSN="user:password@tcp(127.0.0.1:3306)/tempmail?charset=utf8mb4&parseTime=True&loc=Local" \
  -e MAILBOX_TTL=7d \
  -e EMAIL_TTL=24h \
  -p 8080:8080 \
  -p 25:25 \
  ghcr.io/jbtt-2025/ti-temp-mail-api:latest
```

**PostgreSQL：**（`STORAGE_TYPE` 支持 `postgres` 或 `pg`）

```bash
docker run -d \
  --name ti-temp-mail-api \
  --restart unless-stopped \
  -e MAIL_DOMAIN=example.com \
  -e STORAGE_TYPE=postgres \
  -e DB_DSN="host=127.0.0.1 user=postgres password=password dbname=tempmail port=5432 sslmode=disable" \
  -e MAILBOX_TTL=7d \
  -e EMAIL_TTL=24h \
  -p 8080:8080 \
  -p 25:25 \
  ghcr.io/jbtt-2025/ti-temp-mail-api:latest
```

### 4. Cloudflare D1 模式（Worker API + Go 驱动回收）

仓库自带简单的 Cloudflare Worker 脚本（[worker/worker.js](worker/worker.js)）与表结构（[worker/schema.sql](worker/schema.sql)），使用 `API_TOKEN` 鉴权：

1. **创建 D1 数据库**，将返回的 `database_id` 填入 [worker/wrangler.toml](worker/wrangler.toml)：
   ```bash
   npx wrangler d1 create ti-temp-mail
   ```
2. **初始化表结构**：
   ```bash
   npx wrangler d1 execute ti-temp-mail --remote --file=worker/schema.sql
   ```
3. **配置鉴权 Token 并部署 Worker**：
   ```bash
   npx wrangler secret put API_TOKEN -c worker/wrangler.toml
   npx wrangler deploy -c worker/wrangler.toml
   ```
4. **启动 Go 服务**：
   ```bash
   docker run -d \
     --name ti-temp-mail-api \
     --restart unless-stopped \
     -e MAIL_DOMAIN=example.com \
     -e STORAGE_TYPE=d1 \
     -e D1_API_URL=https://ti-temp-mail-d1-worker.your-subdomain.workers.dev \
     -e D1_API_TOKEN=你的API_TOKEN \
     -e MAILBOX_TTL=24h \
     -e EMAIL_TTL=12h \
     -e CLEANUP_INTERVAL=5m \
     -p 8080:8080 \
     -p 25:25 \
     ghcr.io/jbtt-2025/ti-temp-mail-api:latest
   ```

---

## 环境变量

### 基础配置

| 变量 | 默认值 | 说明 |
|---|---|---|
| `MAIL_DOMAIN` | — | **必填**，逗号分隔的接受域名列表，如 `example.com,mail.example.com` |
| `SMTP_PORT` | `25` | SMTP 服务器监听端口（单封邮件上限固定 `20MB`） |
| `HTTP_PORT` | `8080` | HTTP API 服务器监听端口 |
| `CREATE_TOKEN` | — | 可选，设置后创建邮箱接口需携带 `Authorization: <token>` |
| `STORAGE_TYPE` | `memory` | 存储模式：`memory`、`sqlite`、`mysql`、`postgres`（或 `pg`）、`d1` |

### `memory` 模式参数（仅内存模式生效）

| 变量 | 默认值 | 说明 |
|---|---|---|
| `MAX_EMAILS` | `10000` | 最大保留邮件条数（`0` 表示不限条数，按接收时间淘汰最早邮件） |
| `MAX_EMAIL_MEMORY` | `100MB` | 邮件总内存上限（支持 `B`/`KB`/`MB`/`GB` 或纯数字字节，`0` 表示不限总大小；按接收时间淘汰最早邮件） |
| `MAX_MAILBOXES` | `10000` | 最大保留邮箱数（LRU 淘汰） |

> 注：`MAX_EMAILS` 与 `MAX_EMAIL_MEMORY` 不能同时为 `0`。

### 持久化与过期回收参数（`sqlite` / `mysql` / `postgres` / `d1` 生效）

| 变量 | 默认值 | 说明 |
|---|---|---|
| `DB_DSN` | `temp-mail.db`（仅 sqlite） | 数据库连接串；`sqlite` 默认为当前目录 `temp-mail.db`（也兼容 `SQLITE_PATH`），`mysql` / `postgres` 必填 |
| `D1_API_URL` | — | `STORAGE_TYPE=d1` 时必填，Worker 地址 |
| `D1_API_TOKEN` | — | `STORAGE_TYPE=d1` 时必填，对应 Worker 的 `API_TOKEN` |
| `MAILBOX_TTL` | `24h` | 邮箱有效期（支持 `30m`、`24h`、`7d`；`0` 表示永不过期） |
| `EMAIL_TTL` | `24h` | 邮件有效期（支持 `30m`、`24h`、`7d`；`0` 表示永不过期） |
| `CLEANUP_INTERVAL` | `5m` | Go 后台定时清理过期邮箱与邮件的周期 |

### 过期判断说明（动态计算，改配置无需修数据）

持久化表只记录邮箱的 `created_at` 与邮件的 `received_at`，**不持久化 `expires_at`**。
查询与清理时均根据当前配置的 `MAILBOX_TTL` / `EMAIL_TTL` 动态判断（例如 `created_at + MAILBOX_TTL > now`）。因此修改环境变量并重启服务即可立即对历史数据生效，无需迁移或修正数据库记录。

---

## DNS 配置

### 主域名模式

邮箱格式：`random10@example.com`，将根域名的 MX 记录指向本服务：

```
example.com.   MX   10   example.com.
```

### 子域名模式（泛域名）

邮箱格式：`random10@random8.example.com`，需要通配符 MX 记录：

```
*.example.com.   MX   10   example.com.
```

> 部分 DNS 服务商不支持通配符 MX，请确认你的 DNS 提供商支持 `*` 通配符记录。

### 多域名

`MAIL_DOMAIN` 支持逗号分隔多个域名，如 `example.com,example.org`，每个域名单独配置对应的 MX 记录。

---

## API

### 创建邮箱

```
GET /mailbox
POST /mailbox
```

请求体（可选 JSON）：

| 字段 | 类型 | 说明 |
|---|---|---|
| `domain` | string | 指定邮箱域名，不填则从 `MAIL_DOMAIN` 随机选取 |
| `type` | string | `"maindomain"`（默认）或 `"subdomain"` |

示例：

```bash
curl -X POST http://127.0.0.1:8080/mailbox \
  -H "Content-Type: application/json" \
  -d '{"domain":"example.com","type":"subdomain"}'
```

响应 `201`：
```json
{ "token": "uuid", "mailbox": "abc123@xyz456.example.com" }
```

### 获取邮件列表

```
GET /messages
Authorization: <token>
```

示例：

```bash
curl http://127.0.0.1:8080/messages \
  -H "Authorization: <token>"
```

响应 `200`：
```json
{
  "mailbox": "abc123@example.com",
  "messages": [
    {
      "_id": "uuid",
      "receivedAt": 1712345678,
      "from": "sender@example.com",
      "subject": "Hello",
      "bodyPreview": "HTML 正文前 100 个字符...",
      "attachmentsCount": 0
    }
  ]
}
```

### 获取邮件详情

```
GET /messages/{id}
Authorization: <token>
```

示例：

```bash
curl http://127.0.0.1:8080/messages/<id> \
  -H "Authorization: <token>"
```

响应 `200`：
```json
{
  "_id": "uuid",
  "receivedAt": 1712345678,
  "mailbox": "abc123@example.com",
  "from": "sender@example.com",
  "subject": "Hello",
  "bodyPreview": "前 100 个字符...",
  "bodyHtml": "<p>完整 HTML 正文</p>",
  "attachmentsCount": 0,
  "attachments": []
}
```

token 缺失、无效或邮箱已过期返回 `401`，邮件不存在、已过期或不属于该邮箱返回 `404`。
