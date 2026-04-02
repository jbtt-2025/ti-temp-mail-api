package main

import (
	"encoding/json"
	"fmt"
	mathrand "math/rand"
	"net/http"
)

const letters = "abcdefghijklmnopqrstuvwxyz"

func randString(n int) string {
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[mathrand.Intn(len(letters))]
	}
	return string(b)
}


func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{Error: msg})
}

func NewHTTPServer(cfg *Config, ms *MailboxStore, es *EmailStore) *http.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			writeError(w, http.StatusNotFound, "Not Found")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(buildMainHTML(cfg.MailDomains[0])))
	})
	mux.HandleFunc("GET /mailbox", func(w http.ResponseWriter, r *http.Request) {
		handleCreateMailbox(w, r, cfg, ms)
	})
	mux.HandleFunc("POST /mailbox", func(w http.ResponseWriter, r *http.Request) {
		handleCreateMailbox(w, r, cfg, ms)
	})
	mux.HandleFunc("GET /messages", func(w http.ResponseWriter, r *http.Request) {
		handleListMessages(w, r, ms, es)
	})
	mux.HandleFunc("GET /messages/{id}", func(w http.ResponseWriter, r *http.Request) {
		handleGetMessage(w, r, ms, es)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "Not Found")
	})

	return &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.HTTPPort),
		Handler: mux,
	}
}

type createMailboxRequest struct {
	Domain string `json:"domain"`
	Type   string `json:"type"`
}

func handleCreateMailbox(w http.ResponseWriter, r *http.Request, cfg *Config, ms *MailboxStore) {
	// Check CREATE_TOKEN if set
	if cfg.CreateToken != "" {
		if r.Header.Get("Authorization") != cfg.CreateToken {
			writeError(w, http.StatusForbidden, "Forbidden")
			return
		}
	}

	// Parse optional JSON body
	var req createMailboxRequest
	if r.Body != nil && r.ContentLength != 0 {
		json.NewDecoder(r.Body).Decode(&req)
	}

	// Determine domain
	domain := req.Domain
	if domain == "" {
		domain = cfg.MailDomains[mathrand.Intn(len(cfg.MailDomains))]
	}

	// Generate mailbox address with up to 10 retries
	var mailbox string
	for i := 0; i < 10; i++ {
		var addr string
		if req.Type == "subdomain" {
			addr = fmt.Sprintf("%s@%s.%s", randString(10), randString(8), domain)
		} else {
			addr = fmt.Sprintf("%s@%s", randString(10), domain)
		}
		if !ms.Exists(addr) {
			mailbox = addr
			break
		}
	}

	if mailbox == "" {
		writeError(w, http.StatusInternalServerError, "Failed to generate unique mailbox address")
		return
	}

	token := newUUID()
	ms.Set(token, mailbox)

	writeJSON(w, http.StatusCreated, CreateMailboxResponse{
		Token:   token,
		Mailbox: mailbox,
	})
}

func handleListMessages(w http.ResponseWriter, r *http.Request, ms *MailboxStore, es *EmailStore) {
	token := r.Header.Get("Authorization")
	mailbox, ok := ms.GetByToken(token)
	if !ok || token == "" {
		writeError(w, http.StatusUnauthorized, "Unauthorized: Invalid or missing token.")
		return
	}

	emails := es.ListByMailbox(mailbox)
	summaries := make([]MessageSummary, 0, len(emails))
	for _, e := range emails {
		summaries = append(summaries, MessageSummary{
			ID:               e.ID,
			ReceivedAt:       e.ReceivedAt,
			From:             e.From,
			Subject:          e.Subject,
			BodyPreview:      bodyPreview(e.BodyHTML),
			AttachmentsCount: e.AttachmentsCount,
		})
	}

	writeJSON(w, http.StatusOK, ListMessagesResponse{
		Mailbox:  mailbox,
		Messages: summaries,
	})
}

func handleGetMessage(w http.ResponseWriter, r *http.Request, ms *MailboxStore, es *EmailStore) {
	token := r.Header.Get("Authorization")
	mailbox, ok := ms.GetByToken(token)
	if !ok || token == "" {
		writeError(w, http.StatusUnauthorized, "Unauthorized: Invalid or missing token.")
		return
	}

	id := r.PathValue("id")
	email, ok := es.GetByID(id)
	if !ok || email.Mailbox != mailbox {
		writeError(w, http.StatusNotFound, "Message not found or access denied.")
		return
	}

	writeJSON(w, http.StatusOK, MessageDetail{
		ID:               email.ID,
		ReceivedAt:       email.ReceivedAt,
		Mailbox:          email.Mailbox,
		From:             email.From,
		Subject:          email.Subject,
		BodyPreview:      bodyPreview(email.BodyHTML),
		BodyHTML:         email.BodyHTML,
		AttachmentsCount: email.AttachmentsCount,
		Attachments:      []interface{}{},
	})
}

func buildMainHTML(domain string) string {
	return `<!DOCTYPE html>
<html>
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>临时邮箱 API 文档</title>
  <style>
    body { font-family: Arial, sans-serif; max-width: 860px; margin: 0 auto; padding: 20px; line-height: 1.6; }
    h1 { color: #333; }
    h2 { color: #444; margin-top: 30px; }
    h3 { color: #555; margin-top: 20px; }
    code { background: #f4f4f4; padding: 2px 5px; border-radius: 3px; }
    pre { background: #f4f4f4; padding: 15px; border-radius: 5px; overflow-x: auto; }
    .endpoint { margin-bottom: 30px; border-left: 3px solid #ddd; padding-left: 15px; }
    .note { color: #666; }
    table { border-collapse: collapse; width: 100%; }
    th, td { border: 1px solid #ddd; padding: 8px 12px; text-align: left; }
    th { background: #f4f4f4; }
  </style>
</head>
<body>
  <h1>临时邮箱 API</h1>
  <p>自托管临时邮箱服务，内置 SMTP 服务器接收邮件，通过 HTTP API 管理邮箱与邮件。</p>

  <h2>DNS 配置</h2>

  <h3>主域名模式</h3>
  <p>邮箱格式：<code>random10@` + domain + `</code></p>
  <pre>` + domain + `.   MX   10   ` + domain + `.</pre>

  <h3>子域名模式（泛域名）</h3>
  <p>邮箱格式：<code>random10@random8.` + domain + `</code></p>
  <pre>*.` + domain + `.   MX   10   ` + domain + `.</pre>
  <p class="note">注意：部分 DNS 服务商不支持通配符 MX，请确认你的 DNS 提供商支持 <code>*</code> 通配符记录。</p>

  <h2>API 接口</h2>

  <div class="endpoint">
    <h2>1. 创建临时邮箱</h2>
    <p><strong>端点：</strong> <code>GET/POST /mailbox</code></p>
    <p><strong>请求头：</strong></p>
    <pre>Authorization: [CREATE_TOKEN（如果服务端设置了该环境变量）]</pre>
    <p><strong>请求体（可选 JSON）：</strong></p>
    <table>
      <tr><th>字段</th><th>类型</th><th>说明</th></tr>
      <tr><td><code>domain</code></td><td>string</td><td>指定邮箱域名，不填则随机选取</td></tr>
      <tr><td><code>type</code></td><td>string</td><td><code>"maindomain"</code>（默认）或 <code>"subdomain"</code></td></tr>
    </table>
    <pre>{
  "domain": "` + domain + `",
  "type": "subdomain"
}</pre>
    <p><strong>响应示例（201）：</strong></p>
    <pre>{
  "token": "550e8400-e29b-41d4-a716-446655440000",
  "mailbox": "abcdefghij@xxxxxxxx.` + domain + `"
}</pre>
  </div>

  <div class="endpoint">
    <h2>2. 获取邮件列表</h2>
    <p><strong>端点：</strong> <code>GET /messages</code></p>
    <p><strong>请求头：</strong></p>
    <pre>Authorization: [邮箱 token]</pre>
    <p><strong>响应示例（200）：</strong></p>
    <pre>{
  "mailbox": "abcdefghij@` + domain + `",
  "messages": [
    {
      "_id": "550e8400-e29b-41d4-a716-446655440000",
      "receivedAt": 1746356501,
      "from": "sender@example.com",
      "subject": "测试邮件",
      "bodyPreview": "邮件预览内容（最多100字符）",
      "attachmentsCount": 0
    }
  ]
}</pre>
  </div>

  <div class="endpoint">
    <h2>3. 获取邮件详情</h2>
    <p><strong>端点：</strong> <code>GET /messages/{id}</code></p>
    <p><strong>请求头：</strong></p>
    <pre>Authorization: [邮箱 token]</pre>
    <p><strong>响应示例（200）：</strong></p>
    <pre>{
  "_id": "550e8400-e29b-41d4-a716-446655440000",
  "receivedAt": 1746356501,
  "mailbox": "abcdefghij@` + domain + `",
  "from": "sender@example.com",
  "subject": "测试邮件",
  "bodyPreview": "邮件预览内容",
  "bodyHtml": "&lt;div&gt;完整 HTML 正文&lt;/div&gt;",
  "attachmentsCount": 0,
  "attachments": []
}</pre>
  </div>

  <div class="note">
    <h2>注意事项</h2>
    <ul>
      <li>token 在创建邮箱时获取，用于后续所有请求的鉴权</li>
      <li>token 缺失或无效返回 <code>401</code>，邮件不存在或不属于该邮箱返回 <code>404</code></li>
      <li>邮件数据存储在内存中（LRU），重启后清空</li>
    </ul>
  </div>
</body>
</html>`
}
