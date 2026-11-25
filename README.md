# ti-temp-mail-api

基于 Go 实现的自托管临时邮箱服务。内置 SMTP 服务器接收 catch-all 邮件，通过 HTTP API 创建邮箱并读取邮件。默认纯内存运行，也支持 SQLite（无 CGO）、MySQL、PostgreSQL 以及 Cloudflare D1。

---

## 快速启动

> [!TIP]
> `MAIL_DOMAIN` 为邮箱后缀域名（多个用逗号分隔）；`MX_DOMAIN` 为部署本服务的收信域名（可选，不填默认取 `MAIL_DOMAIN` 首个域名），需将 `MX_DOMAIN` 的 `A` / `AAAA` 记录指向运行本容器的机器 IP。

### 1. 默认内存模式（最简单，重启后清空）

```bash
docker run -d \
  --name ti-temp-mail-api \
  --restart unless-stopped \
  -e MAIL_DOMAIN=example.com \
  -e MX_DOMAIN=mx.example.com \
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
  -e MX_DOMAIN=mx.example.com \
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
      MX_DOMAIN: mx.example.com
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
  -e MX_DOMAIN=mx.example.com \
  -e STORAGE_TYPE=mysql \
  -e DB_DSN="user:password@tcp(127.0.0.1:3306)/tempmail?charset=utf8mb4&parseTime=True&loc=Local" \
  -e MAILBOX_TTL=7d \
  -e EMAIL_TTL=24h \
  -p 8080:8080 \
  -p 25:25 \
  ghcr.io/jbtt-2025/ti-temp-mail-api:latest
```

**PostgreSQL：**（`STORAGE_TYPE` 支持 `postgres` 或 `pg`；`DB_DSN` 推荐使用标准 URL 格式，也兼容 `host=... user=...` 键值对格式）

```bash
docker run -d \
  --name ti-temp-mail-api \
  --restart unless-stopped \
  -e MAIL_DOMAIN=example.com \
  -e MX_DOMAIN=mx.example.com \
  -e STORAGE_TYPE=postgres \
  -e DB_DSN="postgres://postgres:password@127.0.0.1:5432/tempmail?sslmode=disable" \
  -e MAILBOX_TTL=7d \
  -e EMAIL_TTL=24h \
  -p 8080:8080 \
  -p 25:25 \
  ghcr.io/jbtt-2025/ti-temp-mail-api:latest
```

### 4. Cloudflare D1 模式（Worker API + Go 驱动回收）

仓库自带轻量的 Cloudflare Worker 脚本（[worker/worker.js](worker/worker.js)）与数据库 SQL（[worker/schema.sql](worker/schema.sql)），使用 `API_TOKEN` 鉴权。支持 **纯网页控制台** 或 **Wrangler CLI** 两种方式部署：

#### 方式 A：Cloudflare 网页控制台部署（零命令行，推荐）

1. **创建 D1 数据库**
   - 登录 Cloudflare Dashboard -> 左侧菜单进入 **Storage & databases -> D1 SQL database** -> 点击 **Create**。
   - 名称填写 `ti-temp-mail`（区域随意），点击创建。
2. **初始化数据库表**
   - 进入建好的 `ti-temp-mail` 数据库详情页 -> 点击顶部 **Console** 标签页。
   - 复制 [worker/schema.sql](worker/schema.sql) 的全部 SQL 粘贴到执行框并点击 **Execute**：
     ```sql
     CREATE TABLE IF NOT EXISTS mailboxes (
       mailbox TEXT PRIMARY KEY,
       token TEXT NOT NULL UNIQUE,
       created_at INTEGER NOT NULL
     );
     CREATE INDEX IF NOT EXISTS idx_mailboxes_created_at ON mailboxes (created_at);

     CREATE TABLE IF NOT EXISTS emails (
       id TEXT PRIMARY KEY,
       mailbox TEXT NOT NULL,
       received_at INTEGER NOT NULL,
       from_addr TEXT NOT NULL DEFAULT '',
       subject TEXT NOT NULL DEFAULT '',
       body_html TEXT NOT NULL DEFAULT '',
       attachments_count INTEGER NOT NULL DEFAULT 0
     );
     CREATE INDEX IF NOT EXISTS idx_emails_mailbox_received ON emails (mailbox, received_at DESC);
     CREATE INDEX IF NOT EXISTS idx_emails_received_at ON emails (received_at);
     ```
3. **创建 Worker 并粘贴脚本**
   - 左侧进入 **Compute (Workers) -> Workers & Pages** -> 点击 **Create -> Create Worker**，名称可填 `ti-temp-mail-d1-worker`，点击 **Deploy**。
   - 点击 **Edit code**，全选清空默认代码，将 [worker/worker.js](worker/worker.js) 完整粘贴进去，点击右上角 **Deploy**。
4. **绑定 D1 数据库与设置 `API_TOKEN`**
   - 回到该 Worker 页面 -> **Settings -> Variables and Secrets**（或 **Bindings**）：
     - 在 **Bindings** 添加 **D1 database**：Variable name 必须填 **`DB`**，选择刚才创建的 `ti-temp-mail`。
     - 在 **Variables and Secrets** 添加变量：Type 选 **Secret**（推荐）或 Text，Variable name 必须填 **`API_TOKEN`**，Value 填入自定义密钥（即 Go 服务端的 `D1_API_TOKEN`）。
   - 保存后若提示有未发布变更，点击部署/生效即可。
5. **获取 Worker 地址并启动 Go 服务**
   - 在 Worker 概览页复制访问地址（如 `https://ti-temp-mail-d1-worker.your-subdomain.workers.dev`，若国内网络访问受限可在 **Settings -> Domains & Routes** 绑定自己的自定义域名），填入下方 `D1_API_URL` 与 `D1_API_TOKEN`。

#### 方式 B：Wrangler CLI 部署

1. **创建 D1 数据库**，将返回的 `database_id` 填入 [worker/wrangler.toml](worker/wrangler.toml)：
   ```bash
   npx wrangler d1 create ti-temp-mail
   ```
2. **执行建表 SQL**：
   ```bash
   npx wrangler d1 execute ti-temp-mail --remote --file=worker/schema.sql
   ```
3. **配置鉴权 Token 并部署 Worker**：
   ```bash
   npx wrangler secret put API_TOKEN -c worker/wrangler.toml
   npx wrangler deploy -c worker/wrangler.toml
   ```

#### 启动 Go 服务连接 D1

```bash
docker run -d \
  --name ti-temp-mail-api \
  --restart unless-stopped \
  -e MAIL_DOMAIN=example.com \
  -e MX_DOMAIN=mx.example.com \
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
| `MAIL_DOMAIN` | — | **必填**，逗号分隔的默认接受域名列表，如 `example.com,mail.example.com`（接口未传 `domain` 时从中随机选取，也用于首页/ `/info` 展示；只要 MX 解析到本机，仍支持通过 `domain` / `mailbox` 参数使用自定义域名） |
| `MX_DOMAIN` | `MAIL_DOMAIN` 首个域名 | 邮件服务器 MX 目标域名，需通过 `A` / `AAAA` 记录指向运行本服务的机器 IP，同时用于首页 DNS 解析教程 |
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
| `DB_DSN` | `temp-mail.db`（仅 sqlite） | 数据库连接串；`sqlite` 默认为当前目录 `temp-mail.db`（也兼容 `SQLITE_PATH`），`mysql` / `postgres` 必填（PG 支持 `postgres://user:pass@host:5432/db?sslmode=disable` 或 `postgresql://...`） |
| `D1_API_URL` | — | `STORAGE_TYPE=d1` 时必填，Worker 地址 |
| `D1_API_TOKEN` | — | `STORAGE_TYPE=d1` 时必填，对应 Worker 的 `API_TOKEN` |
| `MAILBOX_TTL` | `24h` | 邮箱有效期（支持 `30m`、`24h`、`7d`；`0` 表示永不过期；调整后重启立即生效） |
| `EMAIL_TTL` | `24h` | 邮件有效期（支持 `30m`、`24h`、`7d`；`0` 表示永不过期；调整后重启立即生效） |
| `CLEANUP_INTERVAL` | `5m` | 定时清理过期邮箱与邮件的周期 |

---

## DNS 配置

### 1. 解析邮件服务器域名（`MX_DOMAIN`）

`MX` 记录不能直接填写 IP 地址，必须指向一个域名。请先配置 `MX_DOMAIN`（例如 `mx.example.com`；不设置时默认取 `MAIL_DOMAIN` 的第一个域名），并将其 `A` / `AAAA` 记录指向运行本服务的机器公网 IP：

```
mx.example.com.   A      你的服务器IPv4
mx.example.com.   AAAA   你的服务器IPv6（可选）
```

> 若该域名托管在 Cloudflare，请务必关闭代理（保持**仅 DNS / 灰云**），否则 25 端口 SMTP 流量无法直达机器。

### 2. 主域名模式

邮箱格式：`random10@example.com`，将域名 `MX` 记录指向 `MX_DOMAIN`：

```
example.com.      MX   10   mx.example.com.
```

### 3. 子域名模式（泛域名）

邮箱格式：`random10@random8.example.com`，将泛域名 `MX` 记录指向 `MX_DOMAIN`：

```
*.example.com.    MX   10   mx.example.com.
```

> 部分 DNS 服务商不支持通配符 MX，请确认你的 DNS 提供商支持 `*` 通配符记录。

### 4. 多域名

`MAIL_DOMAIN` 支持逗号分隔多个域名（如 `example.com,example.org`）；其他已将 `MX` 指向同一个 `MX_DOMAIN` 的自定义域名也可在接口 `domain` / `mailbox` 中直接使用，无需为每个域名单独配置 `A` / `AAAA` 记录。

---

## API

HTTP 服务已内置 CORS 跨域响应头，支持纯静态前端从任意域名直接调用。

### 获取服务端配置信息

```
GET /info
```

响应 `200`：
```json
{ "domains": ["example.com"], "requiresCreateToken": false }
```

### 创建 / 重申邮箱

```
GET /mailbox
POST /mailbox
```

请求体（可选 JSON）：

| 字段 | 类型 | 说明 |
|---|---|---|
| `domain` | string | 指定邮箱域名，不填则从 `MAIL_DOMAIN` 随机选取 |
| `type` | string | `"maindomain"`（默认）或 `"subdomain"` |
| `mailbox` | string | 可选，指定完整邮箱地址。若该邮箱仍在有效期内（尚未释放），禁止重复申请并返回 `409`；若已过期释放，可用于重新申请新 `token` |

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

---

## 纯静态前端 Web（`frontend/`）

仓库在 [`frontend/`](frontend/index.html) 目录下提供零构建依赖的纯静态前端工作台（无 Node 构建步骤，HTML + CSS + 原生 JS）：

- **上游服务与鉴权配置**：默认上游地址为 `https://anymail.77669876.xyz`，支持自定义上游 URL、可选 `CREATE_TOKEN`（可为空），支持一键探测 `/info`。
- **多域名与模式批量申请**：支持手动填写多个域名（逗号/换行/空格分隔），提供下拉框选择 **自动轮换** / **随机抽取** / **服务端默认** / **指定具体域名**，并支持主域名、子域名或主/子域轮换批量申请多个邮箱。
- **浏览器本地持久化与邮件清理**：所有邮箱地址、`MAIL_TOKEN`、历史邮件正文与验证码均保存于浏览器 `IndexedDB`（配置项存于 `localStorage`），支持安全沙箱 HTML 预览、验证码雷达自动提取、自动轮询，以及按时间一键清理 / 定期自动清理本地邮件缓存（`1h` / `24h` / `3d` / `7d` / `30d` / 全部）。
- **配置与凭证导入导出（忽略本地邮件）**：
  - 导出支持 `.jsonl`（首行可选包含前端配置 `_type: "settings"`，后续每行含 `upstream`、`token`、`mail`、`mail_token`、`mode`、`tags`）、`.json` 配置与凭证包、四段式文本等格式，自动忽略本地缓存邮件。
  - 导入支持拖拽/选择 `.jsonl` / `.json` 文件或直接粘贴文本，可同时恢复前端配置（上游地址、TOKEN、域名与策略、轮询及清理设置）并导入邮箱凭证。
- **Token 失效重申**：当邮箱 Token 失效（如过期或被释放后返回 `401`）时，可在邮箱管理页或邮件面板一键 **重新申请 Token**；若该邮箱仍在有效期内（尚未释放），服务端返回 `409` 拒绝重复申请；若已过期释放，则为其签发新 `MAIL_TOKEN`。

### 使用方式

- **直接本地打开**：双击或用浏览器打开 `frontend/index.html` 即可运行。
- **静态托管**：可直接将 `frontend/` 目录部署到 Cloudflare Pages、GitHub Pages（自带手动触发工作流 [.github/workflows/pages.yml](.github/workflows/pages.yml)）或 Nginx。

---

## GitHub Actions

- [.github/workflows/ci.yml](.github/workflows/ci.yml)：在 `main` / `master` 及 PR 上运行 Go 测试、构建检查以及 `frontend/` 语法与单元测试。
- [.github/workflows/docker-publish.yml](.github/workflows/docker-publish.yml)：推送 Tag 或手动触发时自动完成测试、5 平台静态二进制打包、纯静态前端/Worker 压缩包生成、多架构 Docker 镜像推送与 GitHub Release 发布。
- [.github/workflows/pages.yml](.github/workflows/pages.yml)：支持手动一键将 `frontend/` 发布到 GitHub Pages。
