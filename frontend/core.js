(function (root, factory) {
  if (typeof module === 'object' && module.exports) {
    module.exports = factory();
  } else {
    root.TiCore = factory();
  }
})(typeof globalThis !== 'undefined' ? globalThis : this, function () {
  'use strict';

  const DEFAULT_UPSTREAM_URL = 'https://anymail.77669876.xyz';

  const CLEANUP_AGE_MS = {
    '1h': 60 * 60 * 1000,
    '24h': 24 * 60 * 60 * 1000,
    '3d': 3 * 24 * 60 * 60 * 1000,
    '7d': 7 * 24 * 60 * 60 * 1000,
    '30d': 30 * 24 * 60 * 60 * 1000,
  };

  const VALID_RETENTION_KEYS = new Set(['off', ...Object.keys(CLEANUP_AGE_MS)]);

  const DEFAULT_SETTINGS = {
    upstreamUrl: DEFAULT_UPSTREAM_URL,
    createToken: '',
    domainsRaw: '',
    domainStrategy: 'rotate',
    mailMode: 'maindomain',
    batchCount: 1,
    batchTags: '',
    rotateCursor: 0,
    pollInterval: 30,
    concurrency: 3,
    darkMode: false,
    emailRetention: 'off',
  };

  /**
   * Normalize user-supplied upstream URL by trimming whitespace and trailing slashes.
   * Preserves valid http/https URLs; prepends https:// when scheme is omitted.
   */
  function normalizeUpstreamUrl(raw) {
    let s = String(raw || '').trim();
    if (!s) return DEFAULT_UPSTREAM_URL;
    if (!/^https?:\/\//i.test(s)) {
      s = 'https://' + s;
    }
    s = s.replace(/\/+$/, '');
    return s;
  }

  /**
   * Build deterministic storage key for a mailbox bound to a specific upstream.
   */
  function makeAccountId(upstream, mail) {
    return `${normalizeUpstreamUrl(upstream)}::${String(mail || '').trim().toLowerCase()}`;
  }

  /**
   * Parse custom domain list from comma/semicolon/whitespace/newline separated input.
   */
  function parseDomainsInput(raw) {
    if (Array.isArray(raw)) {
      raw = raw.join('\n');
    }
    const parts = String(raw || '')
      .split(/[\s,;，；]+/)
      .map((item) =>
        item
          .trim()
          .toLowerCase()
          .replace(/^https?:\/\//i, '')
          .replace(/\/.*$/, '')
          .replace(/^\.+|\.+$/g, '')
      )
      .filter(Boolean);

    const seen = new Set();
    const result = [];
    for (const d of parts) {
      if (!seen.has(d) && !d.includes('..') && /^[a-z0-9-]+(?:\.[a-z0-9-]+)+$/i.test(d)) {
        seen.add(d);
        result.push(d);
      }
    }
    return result;
  }

  /**
   * Select domain and next round-robin cursor according to user strategy:
   * - 'rotate': cycles through parsed domains in order
   * - 'random': picks randomly from parsed domains
   * - 'server': omits domain so the upstream server chooses
   * - 'exact:<domain>': always uses the chosen domain
   */
  function pickDomainForRequest({ domains, strategy, cursor = 0, rng = Math.random }) {
    const list = Array.isArray(domains) ? domains : [];
    const strat = String(strategy || 'rotate');

    if (strat.startsWith('exact:')) {
      const target = strat.slice('exact:'.length).trim().toLowerCase();
      return { domain: target || undefined, nextCursor: cursor };
    }

    if (strat === 'server' || list.length === 0) {
      return { domain: undefined, nextCursor: cursor };
    }

    if (strat === 'random') {
      const idx = Math.min(list.length - 1, Math.floor(rng() * list.length));
      return { domain: list[idx], nextCursor: cursor };
    }

    // Default: 'rotate' (round-robin)
    const normalizedIdx = ((cursor % list.length) + list.length) % list.length;
    return {
      domain: list[normalizedIdx],
      nextCursor: (normalizedIdx + 1) % list.length,
    };
  }

  /**
   * Resolve mailbox creation mode ('maindomain' | 'subdomain'), supporting 'alternate'.
   */
  function pickModeForRequest({ mode, index = 0 }) {
    if (mode === 'subdomain') return 'subdomain';
    if (mode === 'alternate') return index % 2 === 0 ? 'maindomain' : 'subdomain';
    return 'maindomain';
  }

  /**
   * Normalize settings object cleanly with defaults and bounds.
   */
  function normalizeSettings(raw, fallback = DEFAULT_SETTINGS) {
    const base = { ...DEFAULT_SETTINGS, ...(fallback || {}) };
    const src = raw && typeof raw === 'object' ? raw : {};

    const upstreamRaw = src.upstreamUrl ?? src.upstream ?? src.upstream_url ?? base.upstreamUrl;
    const tokenRaw = src.createToken ?? src.token ?? src.create_token;
    const domainsRaw = src.domainsRaw ?? src.domains;
    const retentionRaw = src.emailRetention !== undefined ? String(src.emailRetention) : base.emailRetention;
    const modeRaw = src.mailMode !== undefined ? String(src.mailMode) : base.mailMode;

    const batchCount = Number(src.batchCount ?? base.batchCount);
    const pollInterval = Number(src.pollInterval ?? base.pollInterval);
    const concurrency = Number(src.concurrency ?? base.concurrency);
    const rotateCursor = Number(src.rotateCursor ?? base.rotateCursor);

    return {
      upstreamUrl: normalizeUpstreamUrl(upstreamRaw),
      createToken: tokenRaw !== undefined && tokenRaw !== null ? String(tokenRaw) : String(base.createToken || ''),
      domainsRaw: Array.isArray(domainsRaw)
        ? domainsRaw.join(', ')
        : domainsRaw !== undefined && domainsRaw !== null
          ? String(domainsRaw)
          : String(base.domainsRaw || ''),
      domainStrategy: String(src.domainStrategy || base.domainStrategy || 'rotate'),
      mailMode: ['maindomain', 'subdomain', 'alternate'].includes(modeRaw) ? modeRaw : 'maindomain',
      batchCount: Number.isFinite(batchCount) ? Math.max(1, Math.min(100, Math.trunc(batchCount))) : base.batchCount,
      batchTags: src.batchTags !== undefined && src.batchTags !== null ? String(src.batchTags) : String(base.batchTags || ''),
      rotateCursor: Number.isFinite(rotateCursor) && rotateCursor >= 0 ? Math.trunc(rotateCursor) : 0,
      pollInterval: Number.isFinite(pollInterval) ? Math.max(5, Math.min(3600, Math.trunc(pollInterval))) : base.pollInterval,
      concurrency: Number.isFinite(concurrency) ? Math.max(1, Math.min(10, Math.trunc(concurrency))) : base.concurrency,
      darkMode: typeof src.darkMode === 'boolean' ? src.darkMode : Boolean(base.darkMode),
      emailRetention: VALID_RETENTION_KEYS.has(retentionRaw) ? retentionRaw : 'off',
    };
  }

  /**
   * Canonical export shape for one mailbox account (explicitly excludes cached emails).
   */
  function serializeAccountRecord(acc) {
    return {
      upstream: normalizeUpstreamUrl(acc.upstream),
      token: acc.token != null ? String(acc.token) : '',
      mail: String(acc.mail || '').trim(),
      mail_token: String(acc.mail_token || ''),
      mode: acc.mode || 'maindomain',
      tags: Array.isArray(acc.tags) ? acc.tags : [],
      created_at: acc.created_at || Date.now(),
    };
  }

  /**
   * Serialize accounts (and optional frontend settings) to JSON Lines, ignoring cached emails.
   */
  function serializeJsonl(accounts, settings = null) {
    const lines = [];
    if (settings && typeof settings === 'object') {
      lines.push(JSON.stringify({ _type: 'settings', settings: normalizeSettings(settings) }));
    }
    if (Array.isArray(accounts)) {
      for (const acc of accounts) {
        lines.push(JSON.stringify(serializeAccountRecord(acc)));
      }
    }
    return lines.join('\n');
  }

  /**
   * Format accounts + optional frontend settings according to export format (ignores emails).
   */
  function serializeExportData({ accounts = [], settings = null, includeSettings = true, format = 'jsonl' } = {}) {
    const list = Array.isArray(accounts) ? accounts : [];
    const normalizedSettings = includeSettings && settings ? normalizeSettings(settings) : null;

    if (format === 'jsonl') {
      if (!normalizedSettings && list.length === 0) return '';
      return serializeJsonl(list, normalizedSettings);
    }

    if (format === 'json') {
      if (!normalizedSettings && list.length === 0) return '';
      if (normalizedSettings) {
        return JSON.stringify(
          {
            version: 1,
            exported_at: new Date().toISOString(),
            settings: normalizedSettings,
            accounts: list.map(serializeAccountRecord),
          },
          null,
          2
        );
      }
      return JSON.stringify(list.map(serializeAccountRecord), null, 2);
    }

    if (list.length === 0) return '';
    if (format === 'delim') {
      return list.map((a) => `${normalizeUpstreamUrl(a.upstream)}----${a.token || ''}----${a.mail}----${a.mail_token || ''}`).join('\n');
    }
    return list.map((a) => String(a.mail || '').trim()).filter(Boolean).join('\n');
  }

  /**
   * Normalize one parsed import object into our canonical account shape.
   */
  function normalizeImportedRecord(obj, defaultUpstream, defaultToken) {
    if (!obj || typeof obj !== 'object') return null;

    const mailRaw = obj.mail ?? obj.mailbox ?? obj.email ?? obj.address ?? '';
    const mail = String(mailRaw).trim().toLowerCase();
    if (!mail || !mail.includes('@')) return null;

    const upstreamRaw =
      obj.upstream ?? obj.upstreamUrl ?? obj.upstream_url ?? obj.url ?? obj.baseUrl ?? obj.server ?? defaultUpstream;
    const upstream = normalizeUpstreamUrl(upstreamRaw);

    const tokenRaw = obj.token ?? obj.create_token ?? obj.createToken ?? obj.apiToken;
    const token = tokenRaw !== undefined && tokenRaw !== null ? String(tokenRaw) : String(defaultToken || '');

    const mailTokenRaw = obj.mail_token ?? obj.mailToken ?? obj.mailbox_token ?? obj.mailboxToken ?? obj.authToken ?? '';
    const mail_token = String(mailTokenRaw).trim();

    let tags = [];
    if (Array.isArray(obj.tags)) {
      tags = obj.tags.map((t) => String(t).trim()).filter(Boolean);
    } else if (typeof obj.tags === 'string' && obj.tags.trim()) {
      tags = obj.tags.split(/[,\s]+/).map((t) => t.trim()).filter(Boolean);
    }

    const mode = obj.mode === 'subdomain' ? 'subdomain' : 'maindomain';
    const created_at = Number.isFinite(Number(obj.created_at)) ? Number(obj.created_at) : Date.now();

    return {
      id: makeAccountId(upstream, mail),
      upstream,
      token,
      mail,
      mail_token,
      mode,
      tags,
      status: mail_token ? 'idle' : 'expired',
      dead_reason: mail_token ? '' : '缺少 mail_token，请重新申请',
      created_at,
    };
  }

  /**
   * Parse batch import content (.jsonl, JSON bundle/array, or delim lines), ignoring emails.
   * Returns { accounts, settings, errors }.
   */
  function parseJsonlImport(text, { defaultUpstream = DEFAULT_UPSTREAM_URL, defaultToken = '' } = {}) {
    const raw = String(text || '').trim();
    const accounts = [];
    const errors = [];
    let settings = null;
    if (!raw) return { accounts, settings, errors };

    let fallbackUpstream = normalizeUpstreamUrl(defaultUpstream);
    let fallbackToken = String(defaultToken || '');

    const applyParsedSettings = (candidate) => {
      if (!candidate || typeof candidate !== 'object') return;
      settings = normalizeSettings(candidate, settings || DEFAULT_SETTINGS);
      if (settings.upstreamUrl && (!defaultUpstream || defaultUpstream === DEFAULT_UPSTREAM_URL)) {
        fallbackUpstream = settings.upstreamUrl;
      }
      if (settings.createToken && !defaultToken) {
        fallbackToken = settings.createToken;
      }
    };

    const isSettingsEnvelope = (obj) =>
      obj &&
      typeof obj === 'object' &&
      (obj._type === 'settings' ||
        obj.type === 'settings' ||
        obj._type === 'config' ||
        obj.type === 'config' ||
        (obj.settings && typeof obj.settings === 'object') ||
        (obj.config && typeof obj.config === 'object') ||
        (!obj.mail && !obj.mailbox && !obj.email && !obj.address && ('upstreamUrl' in obj || 'domainsRaw' in obj || 'domainStrategy' in obj)));

    const isEmailRecordEnvelope = (obj) =>
      obj &&
      typeof obj === 'object' &&
      (obj._type === 'email' || obj.type === 'email' || (obj.uid && obj.account_id && !obj.mail_token));

    const handleParsedObject = (obj, label) => {
      if (!obj || typeof obj !== 'object') {
        errors.push(`${label} 不是有效的 JSON 对象`);
        return;
      }
      if (isEmailRecordEnvelope(obj)) {
        return; // Ignore email items per specification.
      }
      // Full backup bundle: { settings?, accounts?, emails? (ignored) }
      if (Array.isArray(obj.accounts) || (obj.settings && typeof obj.settings === 'object') || (obj.config && typeof obj.config === 'object')) {
        if (obj.settings || obj.config) {
          applyParsedSettings(obj.settings || obj.config);
        }
        if (Array.isArray(obj.accounts)) {
          obj.accounts.forEach((item, i) => {
            const rec = normalizeImportedRecord(item, fallbackUpstream, fallbackToken);
            if (rec) accounts.push(rec);
            else errors.push(`${label} 中第 ${i + 1} 个账号缺少有效的 mail 字段`);
          });
        }
        return;
      }
      if (isSettingsEnvelope(obj)) {
        applyParsedSettings(obj.settings || obj.config || obj);
        return;
      }
      const rec = normalizeImportedRecord(obj, fallbackUpstream, fallbackToken);
      if (rec) accounts.push(rec);
      else errors.push(`${label} 缺少有效的 mail 字段`);
    };

    // Fast path: JSON object (bundle / settings / single record) or JSON array
    if (raw.startsWith('[')) {
      try {
        const arr = JSON.parse(raw);
        if (Array.isArray(arr)) {
          arr.forEach((item, idx) => handleParsedObject(item, `第 ${idx + 1} 项`));
          return { accounts, settings, errors };
        }
      } catch (err) {
        errors.push(`JSON 数组解析失败: ${err.message}`);
        return { accounts, settings, errors };
      }
    }

    if (raw.startsWith('{') && !raw.includes('\n')) {
      try {
        const single = JSON.parse(raw);
        handleParsedObject(single, '第 1 行');
        return { accounts, settings, errors };
      } catch {
        // Fall through to line-by-line parser to report exact line syntax error.
      }
    } else if (raw.startsWith('{')) {
      try {
        const maybeWholeObj = JSON.parse(raw);
        if (maybeWholeObj && typeof maybeWholeObj === 'object' && !Array.isArray(maybeWholeObj)) {
          handleParsedObject(maybeWholeObj, '备份 JSON');
          return { accounts, settings, errors };
        }
      } catch {
        // Multi-line JSONL falls through to line-by-line parsing cleanly.
      }
    }

    const lines = raw.split(/\r?\n/);
    lines.forEach((line, idx) => {
      const trimmed = line.trim();
      if (!trimmed || trimmed.startsWith('#') || trimmed.startsWith('//')) return;

      if (trimmed.startsWith('{')) {
        try {
          const obj = JSON.parse(trimmed);
          handleParsedObject(obj, `第 ${idx + 1} 行`);
        } catch (err) {
          errors.push(`第 ${idx + 1} 行 JSON 解析失败: ${err.message}`);
        }
        return;
      }

      // Support convenient delimited text: mail----mail_token or upstream----token----mail----mail_token
      if (trimmed.includes('----')) {
        const cols = trimmed.split('----').map((s) => s.trim());
        let candidate = null;
        if (cols.length >= 4 && cols[2].includes('@')) {
          candidate = { upstream: cols[0] || fallbackUpstream, token: cols[1], mail: cols[2], mail_token: cols[3] || '' };
        } else if (cols.length >= 2 && cols[0].includes('@')) {
          candidate = { upstream: fallbackUpstream, token: fallbackToken, mail: cols[0], mail_token: cols[1] || '' };
        }
        const rec = normalizeImportedRecord(candidate, fallbackUpstream, fallbackToken);
        if (rec) accounts.push(rec);
        else errors.push(`第 ${idx + 1} 行分隔格式无效`);
        return;
      }

      errors.push(`第 ${idx + 1} 行无法识别格式（需为 JSONL 对象或分隔文本）`);
    });

    return { accounts, settings, errors };
  }

  /**
   * Convert HTML body into readable plain text for summaries and code detection.
   */
  function stripHtmlToText(html) {
    if (!html) return '';
    return String(html)
      .replace(/<style[\s\S]*?<\/style>/gi, ' ')
      .replace(/<script[\s\S]*?<\/script>/gi, ' ')
      .replace(/<br\s*\/?>/gi, '\n')
      .replace(/<\/(p|div|li|tr|h[1-6])>/gi, '\n')
      .replace(/<[^>]+>/g, ' ')
      .replace(/&nbsp;/gi, ' ')
      .replace(/&amp;/gi, '&')
      .replace(/&lt;/gi, '<')
      .replace(/&gt;/gi, '>')
      .replace(/&quot;/gi, '"')
      .replace(/&#39;/gi, "'")
      .replace(/[ \t]+/g, ' ')
      .replace(/\n{3,}/g, '\n\n')
      .trim();
  }

  /**
   * Sanitize HTML rendered inside sandboxed iframe as defense-in-depth alongside sandbox="".
   */
  function sanitizeEmailHtml(html) {
    if (!html) return '';
    return String(html)
      .replace(/<script[\s\S]*?<\/script>/gi, '')
      .replace(/<iframe[\s\S]*?<\/iframe>/gi, '')
      .replace(/<object[\s\S]*?<\/object>/gi, '')
      .replace(/<embed[\s\S]*?>/gi, '')
      .replace(/<meta[^>]*http-equiv\s*=\s*['"]?refresh[^>]*>/gi, '')
      .replace(/\son[a-z]+\s*=\s*(?:"[^"]*"|'[^']*'|[^\s>]+)/gi, '')
      .replace(/\s(href|src)\s*=\s*(["'])\s*javascript:[\s\S]*?\2/gi, ' $1="#"');
  }

  const STOP_WORDS = new Set([
    'here', 'your', 'this', 'from', 'with', 'have', 'when', 'what', 'then',
    'some', 'into', 'only', 'also', 'more', 'mail', 'team', 'code', 'help',
    'user', 'pass', 'view', 'click', 'open', 'send', 'sent', 'read', 'free',
    'true', 'false', 'null', 'html', 'body', 'head', 'font', 'size'
  ]);

  const OTP_KEYWORDS = [
    '验证码', '动态码', '校验码', '安全码', '确认码',
    'verification code', 'verify code', 'security code', 'confirmation code',
    'one-time passcode', 'one-time password', 'passcode', 'otp', 'pin',
    'auth code', 'access code'
  ];

  function isValidCodeCandidate(val) {
    if (!val || val.length < 4 || val.length > 8) return false;
    if (STOP_WORDS.has(val.toLowerCase())) return false;
    if (/^[0-9]{4}$/.test(val)) {
      const n = parseInt(val, 10);
      if (n >= 1990 && n <= 2035) return false;
    }
    // Avoid pure all-letter English words when checking alphanumeric candidates unless digit exists
    if (/^[A-Za-z]+$/.test(val) && val.length >= 5) return false;
    return true;
  }

  /**
   * Extract verification / OTP code (4-8 digits or short alphanumeric code) from email fields.
   */
  function extractVerificationCode(subject, textPreview, bodyHtml) {
    const plain = textPreview || stripHtmlToText(bodyHtml);
    const textToScan = `${subject || ''}\n${plain}`;

    const explicitRegexes = [
      /(?:验证码|动态码|校验码|安全码|确认码|otp|passcode|pin)(?:\s+is|\s*为|\s*是)?[:：\t ]*([A-Za-z0-9]{4,8})\b/i,
      /(?:verification|confirmation|security|auth|access)\s+code(?:\s+is)?[:：\t ]*([A-Za-z0-9]{4,8})\b/i,
      /(?:enter|use|with)\s+(?:code|pin)[:：\t ]*([A-Za-z0-9]{4,8})\b/i,
      /【[^】]+】[^\n]*?([0-9]{4,8})/,
      /([0-9]{4,8})\s*(?:为您的|是您的|是本次)/,
      /(?:code|pin)[:：\t ]+([0-9]{4,8})\b/i,
    ];

    for (const rx of explicitRegexes) {
      const m = textToScan.match(rx);
      if (m && m[1] && isValidCodeCandidate(m[1].trim())) {
        return m[1].trim();
      }
    }

    if (bodyHtml) {
      const htmlPatterns = [
        /<(?:span|strong|b|div|p|td|h[1-6])[^>]*style="[^"]*(?:font-size:\s*(?:[2-4]\d|1[89])px|letter-spacing)[^"]*"[^>]*>\s*([A-Za-z0-9]{4,8})\s*<\//i,
        /<(?:span|strong|b|h[1-4])[^>]*>\s*([0-9]{4,8})\s*<\//i,
      ];
      for (const rx of htmlPatterns) {
        const m = bodyHtml.match(rx);
        if (m && m[1] && isValidCodeCandidate(m[1].trim())) {
          return m[1].trim();
        }
      }
    }

    const lower = textToScan.toLowerCase();
    if (OTP_KEYWORDS.some((k) => lower.includes(k))) {
      const nums = textToScan.match(/\b([0-9]{4,8})\b/g);
      if (nums) {
        for (const num of nums) {
          if (isValidCodeCandidate(num)) return num;
        }
      }
    }

    return '';
  }

  function escapeHtml(str) {
    return String(str || '')
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;');
  }

  function toTimestampMs(ts) {
    if (ts === null || ts === undefined || ts === '') return NaN;
    if (typeof ts === 'number') {
      if (!Number.isFinite(ts)) return NaN;
      return ts < 1e12 ? ts * 1000 : ts;
    }
    const n = Number(ts);
    if (Number.isFinite(n)) {
      return n < 1e12 ? n * 1000 : n;
    }
    return Date.parse(ts);
  }

  /**
   * Partition cached emails into kept vs expired/removable sets based on ageKey ('1h','24h','3d','7d','30d','all').
   */
  function partitionEmailsForCleanup(emails, ageKey, nowMs = Date.now()) {
    const list = Array.isArray(emails) ? emails : [];
    if (ageKey === 'all') {
      return {
        kept: [],
        removed: [...list],
        removedUids: list.map((e) => e && e.uid).filter(Boolean),
      };
    }
    const maxAgeMs = CLEANUP_AGE_MS[ageKey];
    if (!maxAgeMs) {
      return { kept: [...list], removed: [], removedUids: [] };
    }
    const cutoff = nowMs - maxAgeMs;
    const kept = [];
    const removed = [];
    const removedUids = [];
    for (const email of list) {
      const tsMs = toTimestampMs(email && email.received_at);
      if (Number.isFinite(tsMs) && tsMs <= cutoff) {
        removed.push(email);
        if (email && email.uid) removedUids.push(email.uid);
      } else {
        kept.push(email);
      }
    }
    return { kept, removed, removedUids };
  }

  function formatRelativeTime(ts) {
    const ms = toTimestampMs(ts);
    if (!Number.isFinite(ms)) return '';
    const diffSec = Math.max(0, Math.floor((Date.now() - ms) / 1000));
    if (diffSec < 60) return '刚刚';
    if (diffSec < 3600) return `${Math.floor(diffSec / 60)} 分钟前`;
    if (diffSec < 86400) return `${Math.floor(diffSec / 3600)} 小时前`;
    const d = new Date(ms);
    return `${d.getMonth() + 1}/${d.getDate()} ${String(d.getHours()).padStart(2, '0')}:${String(d.getMinutes()).padStart(2, '0')}`;
  }

  function formatDateTime(ts) {
    const ms = toTimestampMs(ts);
    if (!Number.isFinite(ms)) return '-';
    const d = new Date(ms);
    const pad = (n) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())} ${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}`;
  }

  return {
    DEFAULT_UPSTREAM_URL,
    DEFAULT_SETTINGS,
    CLEANUP_AGE_MS,
    normalizeUpstreamUrl,
    normalizeSettings,
    makeAccountId,
    parseDomainsInput,
    pickDomainForRequest,
    pickModeForRequest,
    serializeAccountRecord,
    serializeJsonl,
    serializeExportData,
    parseJsonlImport,
    toTimestampMs,
    partitionEmailsForCleanup,
    stripHtmlToText,
    sanitizeEmailHtml,
    extractVerificationCode,
    escapeHtml,
    formatRelativeTime,
    formatDateTime,
  };
});
