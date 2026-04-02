# ti-temp-mail-api

基于 Go 实现的自托管临时邮箱服务。内置 SMTP 服务器接收 catch-all 邮件，通过 HTTP API 创建邮箱并读取邮件，无需任何外部云服务即可独立运行。

## 环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `MAIL_DOMAIN` | — | **必填**，逗号分隔的接受域名列表，如 `example.com,mail.example.com` |
| `SMTP_PORT` | `25` | SMTP 服务器监听端口 |
| `HTTP_PORT` | `8080` | HTTP API 服务器监听端口 |
| `MAX_EMAILS` | `10000` | 内存中保留的最大邮件数（LRU 淘汰） |
| `MAX_MAILBOXES` | `10000` | 内存中保留的最大邮箱数（LRU 淘汰） |
| `CREATE_TOKEN` | — | 可选，设置后创建邮箱接口需携带 `Authorization: <token>` |

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

## 快速启动

```bash
docker run -d \
  --name ti-temp-mail-api \
  --restart unless-stopped \
  -e MAIL_DOMAIN=example.com \
  -p 8080:8080 \
  -p 25:25 \
  ghcr.io/jbtt-2025/ti-temp-mail-api:latest
```

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

```json
{ "domain": "example.com", "type": "subdomain" }
```
`type` 为 `"maindomain"`（默认）生成 `abc123@example.com`，`"subdomain"` 生成 `abc123@xyz456.example.com`。

响应 `201`：
```json
{ "token": "uuid", "mailbox": "abc123@example.com" }
```

### 获取邮件列表

```
GET /messages
Authorization: <token>
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

token 缺失或无效返回 `401`，邮件不存在或不属于该邮箱返回 `404`。
