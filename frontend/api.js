(function (root) {
  'use strict';

  const core = root.TiCore;

  class ApiError extends Error {
    constructor(status, message, data) {
      super(message || `HTTP ${status}`);
      this.name = 'ApiError';
      this.status = status;
      this.data = data || null;
    }
  }

  async function requestJSON(url, { method = 'GET', headers = {}, body = undefined, timeoutMs = 15000 } = {}) {
    const controller = typeof AbortController !== 'undefined' ? new AbortController() : null;
    const timer = controller ? setTimeout(() => controller.abort(), timeoutMs) : null;

    try {
      const fetchOpts = {
        method,
        headers: { ...headers },
        signal: controller ? controller.signal : undefined,
      };
      if (body !== undefined) {
        fetchOpts.body = typeof body === 'string' ? body : JSON.stringify(body);
        if (!fetchOpts.headers['Content-Type']) {
          fetchOpts.headers['Content-Type'] = 'application/json';
        }
      }

      const resp = await fetch(url, fetchOpts);
      const rawText = await resp.text();
      let data = null;
      if (rawText) {
        try {
          data = JSON.parse(rawText);
        } catch {
          data = { error: rawText };
        }
      }

      if (!resp.ok) {
        const msg = (data && data.error) || `HTTP ${resp.status}`;
        throw new ApiError(resp.status, msg, data);
      }
      return data;
    } catch (err) {
      if (err instanceof ApiError) throw err;
      if (err && err.name === 'AbortError') {
        throw new ApiError(0, '请求超时，请检查上游服务是否可达');
      }
      throw new ApiError(0, `网络或 CORS 请求失败: ${err && err.message ? err.message : String(err)}`);
    } finally {
      if (timer) clearTimeout(timer);
    }
  }

  /**
   * Probe upstream server metadata via GET /info (optional endpoint).
   */
  async function fetchServerInfo(upstreamUrl) {
    const base = core.normalizeUpstreamUrl(upstreamUrl);
    return await requestJSON(`${base}/info`, { timeoutMs: 8000 });
  }

  /**
   * Allocate a new mailbox or re-apply token for an explicit expired mailbox via POST /mailbox.
   */
  async function createMailbox({ upstreamUrl, createToken, domain, type, mailbox }) {
    const base = core.normalizeUpstreamUrl(upstreamUrl);
    const headers = {};
    if (createToken && String(createToken).trim() !== '') {
      headers.Authorization = String(createToken).trim();
    }

    const payload = {};
    if (mailbox) {
      payload.mailbox = String(mailbox).trim().toLowerCase();
    } else {
      if (type) payload.type = type;
      if (domain) payload.domain = domain;
    }

    return await requestJSON(`${base}/mailbox`, {
      method: 'POST',
      headers,
      body: payload,
      timeoutMs: 15000,
    });
  }

  /**
   * List message summaries for a mailbox via GET /messages using MAIL_TOKEN.
   */
  async function listMessages({ upstreamUrl, mailToken }) {
    const base = core.normalizeUpstreamUrl(upstreamUrl);
    return await requestJSON(`${base}/messages`, {
      method: 'GET',
      headers: { Authorization: String(mailToken || '').trim() },
      timeoutMs: 15000,
    });
  }

  /**
   * Fetch full message detail (including bodyHtml) via GET /messages/{id}.
   */
  async function getMessageDetail({ upstreamUrl, mailToken, id }) {
    const base = core.normalizeUpstreamUrl(upstreamUrl);
    return await requestJSON(`${base}/messages/${encodeURIComponent(id)}`, {
      method: 'GET',
      headers: { Authorization: String(mailToken || '').trim() },
      timeoutMs: 15000,
    });
  }

  root.TiApi = {
    ApiError,
    fetchServerInfo,
    createMailbox,
    listMessages,
    getMessageDetail,
  };
})(typeof globalThis !== 'undefined' ? globalThis : window);
