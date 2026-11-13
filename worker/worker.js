function jsonResponse(data, status = 200) {
  return new Response(JSON.stringify(data), {
    status,
    headers: { "Content-Type": "application/json; charset=utf-8" },
  });
}

function verifyToken(authHeader, expectedToken) {
  if (!authHeader || !expectedToken) {
    return false;
  }
  const prefix = "Bearer ";
  const provided = authHeader.startsWith(prefix) ? authHeader.slice(prefix.length) : authHeader;
  const encoder = new TextEncoder();
  const a = encoder.encode(provided);
  const b = encoder.encode(expectedToken);
  if (a.byteLength !== b.byteLength) {
    return false;
  }
  let diff = 0;
  for (let i = 0; i < a.byteLength; i++) {
    diff |= a[i] ^ b[i];
  }
  return diff === 0;
}

function nowSeconds() {
  return Math.floor(Date.now() / 1000);
}

function mapEmailRow(row) {
  return {
    _id: row.id,
    receivedAt: row.received_at,
    mailbox: row.mailbox,
    from: row.from_addr,
    subject: row.subject,
    bodyHtml: row.body_html,
    attachmentsCount: row.attachments_count,
  };
}

function parseOptionalInt(value) {
  if (value === null || value === undefined || value === "") {
    return null;
  }
  const n = Number(value);
  return Number.isFinite(n) ? Math.trunc(n) : null;
}

export default {
  async fetch(request, env) {
    if (!verifyToken(request.headers.get("Authorization"), env.API_TOKEN)) {
      return jsonResponse({ error: "Unauthorized" }, 401);
    }

    const url = new URL(request.url);
    const { pathname, searchParams } = url;

    try {
      if (request.method === "POST" && pathname === "/mailboxes") {
        const body = await request.json();
        const token = String(body.token || "");
        const mailbox = String(body.mailbox || "");
        if (!token || !mailbox) {
          return jsonResponse({ error: "token and mailbox are required" }, 400);
        }
        const createdAt = Number.isFinite(body.createdAt) ? Math.trunc(body.createdAt) : nowSeconds();

        try {
          await env.DB.prepare(
            "INSERT INTO mailboxes (mailbox, token, created_at) VALUES (?, ?, ?)"
          )
            .bind(mailbox, token, createdAt)
            .run();
          return jsonResponse({ created: true }, 201);
        } catch (err) {
          const msg = String(err && err.message ? err.message : err);
          if (msg.includes("UNIQUE") || msg.includes("PRIMARY KEY") || msg.includes("SQLITE_CONSTRAINT")) {
            return jsonResponse({ created: false, error: "mailbox already exists" }, 409);
          }
          throw err;
        }
      }

      if (request.method === "GET" && pathname === "/mailboxes/exists") {
        const address = searchParams.get("address") || "";
        if (!address) {
          return jsonResponse({ exists: false }, 200);
        }
        const minCreatedAt = parseOptionalInt(searchParams.get("minCreatedAt"));
        const stmt = minCreatedAt !== null
          ? env.DB.prepare("SELECT 1 AS ok FROM mailboxes WHERE mailbox = ? AND created_at > ? LIMIT 1").bind(address, minCreatedAt)
          : env.DB.prepare("SELECT 1 AS ok FROM mailboxes WHERE mailbox = ? LIMIT 1").bind(address);
        const row = await stmt.first();
        return jsonResponse({ exists: Boolean(row) }, 200);
      }

      if (pathname === "/mailboxes/by-token" && (request.method === "POST" || request.method === "GET")) {
        let token = "";
        let minCreatedAt = null;
        if (request.method === "POST") {
          const body = await request.json();
          token = String(body.token || "");
          minCreatedAt = parseOptionalInt(body.minCreatedAt);
        } else {
          token = searchParams.get("token") || "";
          minCreatedAt = parseOptionalInt(searchParams.get("minCreatedAt"));
        }
        if (!token) {
          return jsonResponse({ error: "not found" }, 404);
        }
        const stmt = minCreatedAt !== null
          ? env.DB.prepare("SELECT mailbox FROM mailboxes WHERE token = ? AND created_at > ? LIMIT 1").bind(token, minCreatedAt)
          : env.DB.prepare("SELECT mailbox FROM mailboxes WHERE token = ? LIMIT 1").bind(token);
        const row = await stmt.first();
        if (!row) {
          return jsonResponse({ error: "not found" }, 404);
        }
        return jsonResponse({ mailbox: row.mailbox }, 200);
      }

      if (request.method === "POST" && pathname === "/emails") {
        const body = await request.json();
        const id = String(body.id || "");
        const mailbox = String(body.mailbox || "");
        if (!id || !mailbox) {
          return jsonResponse({ error: "id and mailbox are required" }, 400);
        }
        const receivedAt = Number.isFinite(body.receivedAt) ? Math.trunc(body.receivedAt) : nowSeconds();
        const fromAddr = String(body.from || "");
        const subject = String(body.subject || "");
        const bodyHtml = String(body.bodyHtml || "");
        const attachmentsCount = Number.isFinite(body.attachmentsCount) ? Math.trunc(body.attachmentsCount) : 0;

        await env.DB.prepare(
          "INSERT INTO emails (id, mailbox, received_at, from_addr, subject, body_html, attachments_count) VALUES (?, ?, ?, ?, ?, ?, ?)"
        )
          .bind(id, mailbox, receivedAt, fromAddr, subject, bodyHtml, attachmentsCount)
          .run();
        return jsonResponse({ saved: true }, 201);
      }

      if (request.method === "GET" && pathname === "/emails") {
        const mailbox = searchParams.get("mailbox") || "";
        if (!mailbox) {
          return jsonResponse({ emails: [] }, 200);
        }
        const minReceivedAt = parseOptionalInt(searchParams.get("minReceivedAt"));
        const stmt = minReceivedAt !== null
          ? env.DB.prepare(
              "SELECT id, mailbox, received_at, from_addr, subject, body_html, attachments_count FROM emails WHERE mailbox = ? AND received_at > ? ORDER BY received_at DESC, rowid DESC"
            ).bind(mailbox, minReceivedAt)
          : env.DB.prepare(
              "SELECT id, mailbox, received_at, from_addr, subject, body_html, attachments_count FROM emails WHERE mailbox = ? ORDER BY received_at DESC, rowid DESC"
            ).bind(mailbox);
        const res = await stmt.all();
        const rows = Array.isArray(res.results) ? res.results : [];
        return jsonResponse({ emails: rows.map(mapEmailRow) }, 200);
      }

      if (request.method === "GET" && pathname.startsWith("/emails/")) {
        const id = decodeURIComponent(pathname.slice("/emails/".length));
        if (!id) {
          return jsonResponse({ error: "not found" }, 404);
        }
        const minReceivedAt = parseOptionalInt(searchParams.get("minReceivedAt"));
        const stmt = minReceivedAt !== null
          ? env.DB.prepare(
              "SELECT id, mailbox, received_at, from_addr, subject, body_html, attachments_count FROM emails WHERE id = ? AND received_at > ? LIMIT 1"
            ).bind(id, minReceivedAt)
          : env.DB.prepare(
              "SELECT id, mailbox, received_at, from_addr, subject, body_html, attachments_count FROM emails WHERE id = ? LIMIT 1"
            ).bind(id);
        const row = await stmt.first();
        if (!row) {
          return jsonResponse({ error: "not found" }, 404);
        }
        return jsonResponse({ email: mapEmailRow(row) }, 200);
      }

      if (request.method === "POST" && pathname === "/cleanup") {
        let emailBefore = null;
        let mailboxBefore = null;
        try {
          const body = await request.json();
          if (body) {
            emailBefore = parseOptionalInt(body.emailBefore);
            mailboxBefore = parseOptionalInt(body.mailboxBefore);
          }
        } catch {
          // Empty body leaves both cutoffs null (no deletions).
        }

        let deletedEmails = 0;
        let deletedMailboxes = 0;

        if (emailBefore !== null) {
          const emailRes = await env.DB.prepare(
            "DELETE FROM emails WHERE received_at <= ?"
          )
            .bind(emailBefore)
            .run();
          deletedEmails = emailRes.meta?.changes ?? 0;
        }
        if (mailboxBefore !== null) {
          const mboxRes = await env.DB.prepare(
            "DELETE FROM mailboxes WHERE created_at <= ?"
          )
            .bind(mailboxBefore)
            .run();
          deletedMailboxes = mboxRes.meta?.changes ?? 0;
        }

        return jsonResponse({ deletedEmails, deletedMailboxes }, 200);
      }

      return jsonResponse({ error: "Not Found" }, 404);
    } catch (err) {
      return jsonResponse({ error: String(err && err.message ? err.message : err) }, 500);
    }
  },
};
