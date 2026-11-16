(function (root) {
  'use strict';

  const core = root.TiCore;
  const storage = root.TiStorage;
  const api = root.TiApi;

  const el = (id) => document.getElementById(id);

  const state = {
    db: new storage.BrowserMailStore(),
    settings: storage.loadSettings(),
    accounts: [],
    emails: [],
    selectedAccountId: null, // null = aggregated inbox
    selectedEmailUid: null,
    activeFolder: 'inbox', // 'inbox' | 'code_radar' | 'accounts'
    subFilter: 'all', // 'all' | 'unread' | 'code'
    mailSearch: '',
    mailView: 'html', // 'html' | 'text' | 'raw'
    accSearch: '',
    accSort: 'created_desc',
    accStatusFilter: 'all',
    selectedAccountIds: new Set(),
    switcherOpen: false,
    switcherSearch: '',
    isPolling: false,
    pollTimerId: null,
    runningJobs: 0,
    editingAccountId: null,
    parsedImport: { accounts: [], errors: [] },
  };

  function showToast(message, type = 'ok') {
    const container = el('toastContainer');
    const div = document.createElement('div');
    div.className = `toast ${type}`;
    div.textContent = message;
    container.appendChild(div);
    setTimeout(() => {
      div.remove();
    }, 3600);
  }

  async function copyText(text, label = '内容') {
    if (!text) return;
    try {
      await navigator.clipboard.writeText(String(text));
      showToast(`已复制${label}`, 'ok');
    } catch {
      const ta = document.createElement('textarea');
      ta.value = String(text);
      document.body.appendChild(ta);
      ta.select();
      document.execCommand('copy');
      ta.remove();
      showToast(`已复制${label}`, 'ok');
    }
  }

  function applyDarkMode() {
    document.documentElement.classList.toggle('dark', Boolean(state.settings.darkMode));
    el('btnToggleDark').textContent = state.settings.darkMode ? '☀️' : '🌙';
  }

  function saveCurrentSettings() {
    storage.saveSettings(state.settings);
    updateTopbarUpstreamChip();
  }

  function getEmailsByAccountMap() {
    const map = new Map();
    for (const email of state.emails) {
      let arr = map.get(email.account_id);
      if (!arr) {
        arr = [];
        map.set(email.account_id, arr);
      }
      arr.push(email);
    }
    return map;
  }

  function recomputeAccountAggregates() {
    const byAcc = getEmailsByAccountMap();
    for (const acc of state.accounts) {
      const list = byAcc.get(acc.id) || [];
      list.sort((a, b) => (b.received_at || 0) - (a.received_at || 0));
      acc.unread_count = list.filter((m) => !m.is_read).length;
      const withCode = list.find((m) => m.extracted_code);
      acc.latest_code = withCode ? withCode.extracted_code : '';
      if (acc.status !== 'expired' && acc.status !== 'fetching') {
        if (list.length > 0) acc.status = 'active';
        else if (acc.last_checked_at) acc.status = 'empty';
      }
    }
  }

  function statusMeta(status) {
    switch (status) {
      case 'active': return { label: '正常', dot: 'dot-active', badge: 'badge-active' };
      case 'empty': return { label: '空箱', dot: 'dot-empty', badge: 'badge-empty' };
      case 'expired': return { label: 'Token失效', dot: 'dot-expired', badge: 'badge-expired' };
      case 'fetching': return { label: '同步中', dot: 'dot-fetching', badge: 'badge-fetching' };
      default: return { label: '待命', dot: 'dot-idle', badge: 'badge-idle' };
    }
  }

  function strategyDisplayName(strategy, domains) {
    if (!strategy || strategy === 'rotate') {
      return domains.length > 0 ? `轮换(${domains.length}域)` : '轮换(默认)';
    }
    if (strategy === 'random') {
      return domains.length > 0 ? `随机(${domains.length}域)` : '随机';
    }
    if (strategy === 'server') return '服务端默认';
    if (strategy.startsWith('exact:')) return strategy.slice('exact:'.length);
    return strategy;
  }

  function updateTopbarUpstreamChip() {
    const domains = core.parseDomainsInput(state.settings.domainsRaw);
    el('headerUpstreamText').textContent = core.normalizeUpstreamUrl(state.settings.upstreamUrl);
    el('headerDomainBadge').textContent = strategyDisplayName(state.settings.domainStrategy, domains);
  }

  function rebuildDomainStrategyOptions() {
    const select = el('selectDomainStrategy');
    const raw = el('inputDomainsRaw').value;
    const domains = core.parseDomainsInput(raw);
    const prev = state.settings.domainStrategy || 'rotate';

    const opts = [
      {
        value: 'rotate',
        label: domains.length > 0
          ? `🔄 自动轮换（依次使用填写的 ${domains.length} 个域名）`
          : '🔄 自动轮换（若未填域名则由服务端随机）',
      },
      {
        value: 'random',
        label: domains.length > 0
          ? `🎲 随机抽取（从填写的 ${domains.length} 个域名随机）`
          : '🎲 随机抽取（由服务端随机）',
      },
      {
        value: 'server',
        label: '🌐 服务端默认（请求不指定 domain 参数）',
      },
    ];

    domains.forEach((d) => {
      opts.push({ value: `exact:${d}`, label: `📌 指定域名: ${d}` });
    });

    select.innerHTML = opts
      .map((o) => `<option value="${core.escapeHtml(o.value)}">${core.escapeHtml(o.label)}</option>`)
      .join('');

    const validValues = new Set(opts.map((o) => o.value));
    select.value = validValues.has(prev) ? prev : 'rotate';
    state.settings.domainStrategy = select.value;
    updateTopbarUpstreamChip();
  }

  function syncCreateModalFromSettings() {
    el('inputUpstreamUrl').value = state.settings.upstreamUrl || core.DEFAULT_UPSTREAM_URL;
    el('inputCreateToken').value = state.settings.createToken || '';
    el('inputDomainsRaw').value = state.settings.domainsRaw || '';
    el('selectMailMode').value = state.settings.mailMode || 'maindomain';
    el('inputBatchCount').value = state.settings.batchCount || 1;
    el('inputBatchTags').value = state.settings.batchTags || '';
    rebuildDomainStrategyOptions();
  }

  function persistCreateFormToSettings() {
    state.settings.upstreamUrl = core.normalizeUpstreamUrl(el('inputUpstreamUrl').value);
    state.settings.createToken = el('inputCreateToken').value.trim();
    state.settings.domainsRaw = el('inputDomainsRaw').value;
    state.settings.domainStrategy = el('selectDomainStrategy').value || 'rotate';
    state.settings.mailMode = el('selectMailMode').value || 'maindomain';
    state.settings.batchCount = Math.max(1, Math.min(100, Number(el('inputBatchCount').value) || 1));
    state.settings.batchTags = el('inputBatchTags').value.trim();
    saveCurrentSettings();
  }

  function renderAll() {
    recomputeAccountAggregates();
    renderStatsAndSidebar();
    renderSwitcher();
    if (state.activeFolder === 'accounts') {
      el('viewMailColumns').classList.add('hidden');
      el('viewAccountsFull').classList.remove('hidden');
      renderAccountManager();
    } else {
      el('viewAccountsFull').classList.add('hidden');
      el('viewMailColumns').classList.remove('hidden');
      renderMailList();
      renderMailDisplay();
    }
  }

  function renderStatsAndSidebar() {
    const totalAcc = state.accounts.length;
    const totalEmails = state.emails.length;
    const unreadTotal = state.emails.filter((e) => !e.is_read).length;
    const codeTotal = state.emails.filter((e) => Boolean(e.extracted_code)).length;

    const activeCount = state.accounts.filter((a) => a.status === 'active').length;
    const expiredCount = state.accounts.filter((a) => a.status === 'expired').length;
    const emptyCount = state.accounts.filter((a) => a.status === 'empty' || a.status === 'idle').length;

    el('statTotalAccounts').textContent = String(totalAcc);
    el('statTotalEmails').textContent = String(totalEmails);
    el('statExpiredAccounts').textContent = String(expiredCount);
    el('sidebarAccCount').textContent = `${totalAcc} 个邮箱`;
    el('navAccTotal').textContent = String(totalAcc);

    const unreadEl = el('navUnreadCount');
    unreadEl.textContent = String(unreadTotal);
    unreadEl.classList.toggle('hidden', unreadTotal === 0);

    const codeEl = el('navCodeCount');
    codeEl.textContent = `${codeTotal} 条`;
    codeEl.classList.toggle('hidden', codeTotal === 0);

    el('tileActiveCount').textContent = String(activeCount);
    el('tileExpiredCount').textContent = String(expiredCount);
    el('tileEmptyCount').textContent = String(emptyCount);
    el('tileStorageKind').textContent = state.db.usingFallback ? 'Memory' : 'IndexedDB';

    const jobsEl = el('runningJobsLabel');
    jobsEl.textContent = `${state.runningJobs} 运行中`;
    jobsEl.classList.toggle('hidden', state.runningJobs === 0);

    document.querySelectorAll('.nav-btn').forEach((btn) => {
      btn.classList.toggle('active', btn.dataset.folder === state.activeFolder);
    });

    el('btnReapplyAllExpired').disabled = expiredCount === 0 || state.runningJobs > 0;
    el('btnPurgeExpired').disabled = expiredCount === 0;
    el('btnSyncAll').disabled = totalAcc === 0;
    el('btnSyncAll').textContent = state.runningJobs > 0
      ? `⏳ 同步中 (${state.runningJobs})...`
      : '⚡ 一键同步所有';

    el('btnTogglePolling').classList.toggle('on', state.isPolling);
    el('rangeConcurrency').value = String(state.settings.concurrency || 3);
    el('concurrencyVal').textContent = String(state.settings.concurrency || 3);
    el('inputPollInterval').value = String(state.settings.pollInterval || 30);
    document.querySelectorAll('.preset-btn').forEach((btn) => {
      btn.classList.toggle('active', Number(btn.dataset.preset) === Number(state.settings.pollInterval));
    });
  }

  function renderSwitcher() {
    const selectedAcc = state.accounts.find((a) => a.id === state.selectedAccountId) || null;
    if (selectedAcc && !state.selectedAccountId) {
      state.selectedAccountId = null;
    }

    const totalUnread = state.emails.filter((e) => !e.is_read).length;
    const dot = el('switcherCurrentDot');
    const title = el('switcherCurrentTitle');
    const sub = el('switcherCurrentSub');

    if (selectedAcc) {
      const meta = statusMeta(selectedAcc.status);
      dot.className = `dot ${meta.dot}`;
      title.textContent = selectedAcc.mail;
      sub.textContent = `${meta.label}${selectedAcc.unread_count ? ` · ${selectedAcc.unread_count} 未读` : ''}`;
    } else {
      dot.className = 'dot dot-all';
      title.textContent = '聚合收件箱 (全部邮箱)';
      sub.textContent = `${totalUnread} 封未读`;
    }

    el('switcherDropdown').classList.toggle('hidden', !state.switcherOpen);
    if (!state.switcherOpen) return;

    const q = state.switcherSearch.trim().toLowerCase();
    const sorted = [...state.accounts].sort((a, b) => (b.created_at || 0) - (a.created_at || 0));
    const filtered = sorted.filter((acc) => {
      if (!q) return true;
      return (
        acc.mail.toLowerCase().includes(q) ||
        acc.upstream.toLowerCase().includes(q) ||
        (acc.tags || []).some((t) => t.toLowerCase().includes(q))
      );
    });

    const displayed = filtered.slice(0, 80);
    let html = `
      <button class="switcher-item ${selectedAcc === null ? 'active' : ''}" data-select-account="__all__" type="button">
        <div style="display:flex;align-items:center;gap:8px;min-width:0;">
          <span class="dot dot-all"></span>
          <span style="font-size:12px;font-weight:600;">聚合收件箱 (全部邮箱)</span>
        </div>
        <span class="mono" style="font-size:11px;color:var(--text-muted);">${totalUnread} 未读</span>
      </button>
    `;

    for (const acc of displayed) {
      const meta = statusMeta(acc.status);
      const isSelected = selectedAcc && selectedAcc.id === acc.id;
      html += `
        <button class="switcher-item ${isSelected ? 'active' : ''}" data-select-account="${core.escapeHtml(acc.id)}" type="button">
          <div style="display:flex;align-items:center;gap:8px;min-width:0;flex:1;">
            <span class="dot ${meta.dot}"></span>
            <div style="min-width:0;flex:1;">
              <div class="mono" style="font-size:11px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;">${core.escapeHtml(acc.mail)}</div>
              <div style="font-size:10px;color:var(--text-muted);display:flex;gap:6px;align-items:center;">
                <span>${core.escapeHtml(meta.label)}</span>
                ${acc.latest_code ? `<span class="mono" style="color:var(--text-blue);">⚡ ${core.escapeHtml(acc.latest_code)}</span>` : ''}
              </div>
            </div>
          </div>
          ${acc.unread_count ? `<span class="badge badge-blue">${acc.unread_count}</span>` : ''}
        </button>
      `;
    }

    if (filtered.length > 80) {
      html += `<div style="padding:8px;text-align:center;font-size:11px;color:var(--text-muted);">显示前 80 项（共 ${filtered.length} 项），请输入关键词过滤</div>`;
    } else if (filtered.length === 0 && state.accounts.length > 0) {
      html += `<div style="padding:12px;text-align:center;font-size:11px;color:var(--text-muted);">无匹配邮箱</div>`;
    }

    el('switcherList').innerHTML = html;
  }

  function getFilteredEmails() {
    let list = state.emails;
    if (state.selectedAccountId) {
      list = list.filter((e) => e.account_id === state.selectedAccountId);
    }
    if (state.activeFolder === 'code_radar') {
      list = list.filter((e) => Boolean(e.extracted_code));
    }

    const q = state.mailSearch.trim().toLowerCase();
    if (q) {
      list = list.filter((e) =>
        (e.subject || '').toLowerCase().includes(q) ||
        (e.from || '').toLowerCase().includes(q) ||
        (e.mailbox || '').toLowerCase().includes(q) ||
        (e.body_preview || '').toLowerCase().includes(q) ||
        (e.extracted_code || '').toLowerCase().includes(q)
      );
    }

    const counts = {
      all: list.length,
      unread: list.filter((e) => !e.is_read).length,
      code: list.filter((e) => Boolean(e.extracted_code)).length,
    };

    if (state.subFilter === 'unread') list = list.filter((e) => !e.is_read);
    else if (state.subFilter === 'code') list = list.filter((e) => Boolean(e.extracted_code));

    list = [...list].sort((a, b) => (b.received_at || 0) - (a.received_at || 0));
    return { list, counts };
  }

  function renderMailList() {
    const { list, counts } = getFilteredEmails();
    el('subCountAll').textContent = String(counts.all);
    el('subCountUnread').textContent = String(counts.unread);
    el('subCountCode').textContent = String(counts.code);

    document.querySelectorAll('.subtab').forEach((btn) => {
      btn.classList.toggle('active', btn.dataset.subfilter === state.subFilter);
    });

    const container = el('mailListContainer');
    if (list.length === 0) {
      const selectedAcc = state.accounts.find((a) => a.id === state.selectedAccountId);
      container.innerHTML = `
        <div class="empty-state" style="padding:32px 16px;">
          <div style="font-size:28px;margin-bottom:8px;">📭</div>
          <div style="font-size:12px;font-weight:600;color:var(--text-primary);margin-bottom:4px;">
            ${state.accounts.length === 0 ? '暂无本地邮箱' : '当前收件箱为空'}
          </div>
          <div style="font-size:11px;">
            ${selectedAcc
              ? `等待邮件到达 ${core.escapeHtml(selectedAcc.mail)}，点击右上角 ↻ 刷新`
              : state.accounts.length === 0
                ? '请先申请邮箱或导入 JSONL 凭证'
                : '发送邮件到任意已申请邮箱后点击同步'}
          </div>
        </div>
      `;
      return;
    }

    container.innerHTML = list.map((m) => {
      const isSelected = state.selectedEmailUid === m.uid;
      return `
        <div class="mail-card ${isSelected ? 'selected' : ''} ${!m.is_read ? 'unread' : ''}" data-select-email="${core.escapeHtml(m.uid)}">
          <div class="mail-card-top">
            <div style="display:flex;align-items:center;gap:6px;min-width:0;">
              ${!m.is_read ? '<span class="dot dot-all" title="未读"></span>' : ''}
              <span class="mail-card-sender">${core.escapeHtml(m.from || '(未知发件人)')}</span>
            </div>
            <span class="mail-card-time">${core.escapeHtml(core.formatRelativeTime(m.received_at))}</span>
          </div>
          <div class="mail-card-subject">${core.escapeHtml(m.subject || '(无主题)')}</div>
          <div class="mail-card-preview">${core.escapeHtml(m.body_preview || '(无正文内容)')}</div>
          <div class="mail-card-meta">
            <span class="mail-target-pill mono" title="${core.escapeHtml(m.mailbox)}">${core.escapeHtml(m.mailbox)}</span>
            ${m.extracted_code ? `
              <button class="badge badge-blue mono" style="border:0;" data-copy-code="${core.escapeHtml(m.extracted_code)}" title="点击一键复制验证码" type="button">
                ⚡ ${core.escapeHtml(m.extracted_code)}
              </button>
            ` : ''}
          </div>
        </div>
      `;
    }).join('');
  }

  function renderMailDisplay() {
    const container = el('mailDisplayContainer');
    const selectedAcc = state.accounts.find((a) => a.id === state.selectedAccountId) || null;
    const email = state.emails.find((e) => e.uid === state.selectedEmailUid) || null;

    if (state.accounts.length === 0) {
      const domains = core.parseDomainsInput(state.settings.domainsRaw);
      container.innerHTML = `
        <div class="empty-state">
          <div class="welcome-card">
            <h2>⚡ 欢迎使用 Ti-TempMail 临时邮箱工作台</h2>
            <p>纯静态前端应用，所有邮箱地址、MAIL_TOKEN 及收到的邮件正文均持久化保存在当前浏览器内。</p>
            <div style="padding:12px;border-radius:10px;background:var(--bg-subtle);border:1px solid var(--border-color);margin-bottom:16px;font-size:12px;display:flex;flex-direction:column;gap:6px;">
              <div>当前默认上游：<span class="mono" style="color:var(--text-blue);">${core.escapeHtml(core.normalizeUpstreamUrl(state.settings.upstreamUrl))}</span></div>
              <div>域名策略：<b>${core.escapeHtml(strategyDisplayName(state.settings.domainStrategy, domains))}</b> · 模式：<b>${state.settings.mailMode === 'subdomain' ? '子域名' : state.settings.mailMode === 'alternate' ? '主/子域轮换' : '主域名'}</b></div>
            </div>
            <div style="display:flex;flex-wrap:wrap;gap:10px;">
              <button id="btnWelcomeQuickApply" class="btn btn-primary" type="button">🚀 立即申请 1 个邮箱</button>
              <button id="btnWelcomeConfigCreate" class="btn" type="button">⚙️ 配置上游 / 多域名轮换 / 批量申请</button>
              <button id="btnWelcomeImport" class="btn" type="button">📥 导入 JSONL 文件</button>
            </div>
          </div>
        </div>
      `;
      return;
    }

    const emailAcc = email ? state.accounts.find((a) => a.id === email.account_id) : selectedAcc;
    const expiredBanner = emailAcc && emailAcc.status === 'expired'
      ? `
        <div class="alert-expired">
          <div style="font-size:12px;">
            <b>⚠️ ${core.escapeHtml(emailAcc.mail)}</b> 的 MAIL_TOKEN 已失效 (${core.escapeHtml(emailAcc.dead_reason || '401 Unauthorized')})
          </div>
          <div style="display:flex;gap:6px;">
            <button class="btn btn-sm btn-warn" data-reapply-acc="${core.escapeHtml(emailAcc.id)}" type="button">🔄 重新申请 Token</button>
            <button class="btn btn-sm" data-edit-acc="${core.escapeHtml(emailAcc.id)}" type="button">✏️ 编辑上游/TOKEN</button>
          </div>
        </div>
      `
      : '';

    if (!email) {
      container.innerHTML = `
        ${expiredBanner}
        <div class="empty-state">
          <div style="font-size:42px;opacity:0.4;margin-bottom:10px;">✉️</div>
          <div style="font-size:13px;">在左侧列表选择一封邮件，即可在安全沙箱中查看正文与验证码</div>
        </div>
      `;
      return;
    }

    const rawHtml = email.body_html || `<pre style="white-space:pre-wrap;font-family:sans-serif;">${core.escapeHtml(email.body_preview || '')}</pre>`;
    const sanitized = core.sanitizeEmailHtml(rawHtml);
    const srcDocHtml = `<!DOCTYPE html><html><head><meta charset="utf-8"><style>body{font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,Helvetica,Arial,sans-serif;font-size:14px;line-height:1.6;color:#18181b;padding:16px;margin:0;word-break:break-word;}img{max-width:100%;height:auto;}a{color:#2563eb;}</style></head><body>${sanitized}</body></html>`;

    let bodySection = '';
    if (state.mailView === 'html') {
      bodySection = `<iframe id="emailHtmlFrame" class="mail-iframe" sandbox="" referrerpolicy="no-referrer"></iframe>`;
    } else if (state.mailView === 'text') {
      bodySection = `<pre class="mail-pre">${core.escapeHtml(email.body_preview || core.stripHtmlToText(email.body_html) || '(无纯文本内容)')}</pre>`;
    } else {
      bodySection = `<pre class="mail-pre mono">${core.escapeHtml(email.body_html || '(无原始 HTML)')}</pre>`;
    }

    container.innerHTML = `
      ${expiredBanner}
      <div class="display-header">
        <div class="display-title-row">
          <h2 class="display-title">${core.escapeHtml(email.subject || '(无主题)')}</h2>
          <div style="display:flex;gap:6px;flex-shrink:0;">
            <button class="btn btn-sm" data-toggle-read="${core.escapeHtml(email.uid)}" type="button">
              ${email.is_read ? '标为未读' : '标为已读'}
            </button>
            <button class="btn btn-sm btn-danger" data-delete-email="${core.escapeHtml(email.uid)}" type="button">删除</button>
          </div>
        </div>

        <div class="display-meta-row">
          <div style="display:flex;flex-wrap:wrap;gap:6px;align-items:center;">
            <span style="font-weight:600;color:var(--text-primary);">${core.escapeHtml(email.from || '(未知发件人)')}</span>
            <span>·</span>
            <span>收件邮箱：<span class="mono" style="color:var(--text-primary);">${core.escapeHtml(email.mailbox)}</span></span>
          </div>
          <div class="mono">${core.escapeHtml(core.formatDateTime(email.received_at))}</div>
        </div>

        ${email.extracted_code ? `
          <div class="code-card">
            <div style="display:flex;align-items:center;gap:12px;">
              <span style="font-size:12px;font-weight:600;color:var(--text-blue);">⚡ 提取到验证码:</span>
              <span class="code-value mono">${core.escapeHtml(email.extracted_code)}</span>
            </div>
            <button class="btn btn-primary btn-sm" data-copy-code="${core.escapeHtml(email.extracted_code)}" type="button">一键复制</button>
          </div>
        ` : ''}

        <div class="view-tabs">
          <button class="view-tab ${state.mailView === 'html' ? 'active' : ''}" data-mail-view="html" type="button">HTML 沙箱渲染</button>
          <button class="view-tab ${state.mailView === 'text' ? 'active' : ''}" data-mail-view="text" type="button">纯文本预览</button>
          <button class="view-tab ${state.mailView === 'raw' ? 'active' : ''}" data-mail-view="raw" type="button">原始 HTML</button>
        </div>
      </div>

      <div class="display-body">${bodySection}</div>
    `;

    const frame = el('emailHtmlFrame');
    if (frame) {
      frame.srcdoc = srcDocHtml;
    }
  }

  function getFilteredSortedAccounts() {
    const q = state.accSearch.trim().toLowerCase();
    let list = state.accounts.filter((acc) => {
      if (state.accStatusFilter !== 'all' && acc.status !== state.accStatusFilter) return false;
      if (!q) return true;
      return (
        acc.mail.toLowerCase().includes(q) ||
        acc.upstream.toLowerCase().includes(q) ||
        (acc.latest_code || '').toLowerCase().includes(q) ||
        (acc.tags || []).some((t) => t.toLowerCase().includes(q))
      );
    });

    list = [...list].sort((a, b) => {
      switch (state.accSort) {
        case 'created_asc': return (a.created_at || 0) - (b.created_at || 0);
        case 'last_checked_desc': return (b.last_checked_at || 0) - (a.last_checked_at || 0);
        case 'mail_asc': return a.mail.localeCompare(b.mail);
        case 'unread_desc': return (b.unread_count || 0) - (a.unread_count || 0);
        default: return (b.created_at || 0) - (a.created_at || 0);
      }
    });
    return list;
  }

  function renderAccountManager() {
    const list = getFilteredSortedAccounts();
    el('managerSubtitle').textContent = `共 ${state.accounts.length} 个邮箱 · 当前筛选 ${list.length} 个 · 已勾选 ${state.selectedAccountIds.size} 个`;
    el('btnCopyFilteredMails').textContent = `📋 复制邮箱 (${list.length})`;
    el('btnCopyFilteredMails').disabled = list.length === 0;

    const allChecked = list.length > 0 && list.every((a) => state.selectedAccountIds.has(a.id));
    el('chkAllAccounts').checked = allChecked;

    const tbody = el('accTableBody');
    if (list.length === 0) {
      tbody.innerHTML = `
        <tr>
          <td colspan="10" style="text-align:center;padding:42px;color:var(--text-muted);">
            暂无符合条件的邮箱，可点击「+ 批量申请邮箱」或「📥 批量导入」添加
          </td>
        </tr>
      `;
      return;
    }

    tbody.innerHTML = list.map((acc) => {
      const meta = statusMeta(acc.status);
      const checked = state.selectedAccountIds.has(acc.id);
      const tagsHtml = (acc.tags || [])
        .map((t) => `<span class="tag-pill">${core.escapeHtml(t)}</span>`)
        .join(' ');
      const upstreamShort = acc.upstream.replace(/^https?:\/\//i, '');
      return `
        <tr>
          <td><input type="checkbox" class="acc-chk" data-acc-chk="${core.escapeHtml(acc.id)}" ${checked ? 'checked' : ''} /></td>
          <td>
            <span class="badge ${meta.badge}" title="${core.escapeHtml(acc.dead_reason || '')}">
              <span class="dot ${meta.dot}"></span>
              ${core.escapeHtml(meta.label)}
            </span>
          </td>
          <td>
            <div style="display:flex;align-items:center;gap:6px;">
              <button class="mono" style="border:0;background:transparent;padding:0;color:var(--text-primary);font-weight:600;" data-open-acc-inbox="${core.escapeHtml(acc.id)}" type="button" title="点击打开此邮箱收件箱">
                ${core.escapeHtml(acc.mail)}
              </button>
              <button class="btn btn-xs" data-copy-text="${core.escapeHtml(acc.mail)}" data-copy-label="邮箱" type="button" title="复制邮箱">📋</button>
              ${acc.unread_count ? `<span class="badge badge-blue">${acc.unread_count}</span>` : ''}
            </div>
            ${acc.dead_reason ? `<div style="font-size:10px;color:var(--text-rose);margin-top:2px;">${core.escapeHtml(acc.dead_reason)}</div>` : ''}
          </td>
          <td>
            <div class="mono" style="font-size:11px;">${core.escapeHtml(upstreamShort)}</div>
            <div style="font-size:10px;color:var(--text-muted);">CREATE_TOKEN: ${acc.token ? '已配置' : '空'}</div>
          </td>
          <td>${acc.mode === 'subdomain' ? '子域名' : '主域名'}</td>
          <td>
            ${acc.latest_code ? `
              <button class="badge badge-blue mono" style="border:0;" data-copy-code="${core.escapeHtml(acc.latest_code)}" type="button">
                ⚡ ${core.escapeHtml(acc.latest_code)} 📋
              </button>
            ` : '<span style="color:var(--text-muted);">-</span>'}
          </td>
          <td class="mono" style="font-size:11px;color:var(--text-muted);white-space:nowrap;">${core.escapeHtml(core.formatDateTime(acc.created_at))}</td>
          <td class="mono" style="font-size:11px;color:var(--text-muted);white-space:nowrap;">${acc.last_checked_at ? core.escapeHtml(core.formatDateTime(acc.last_checked_at)) : '未同步'}</td>
          <td>
            <div style="display:flex;flex-wrap:wrap;gap:4px;align-items:center;">
              ${tagsHtml}
              <button class="btn btn-xs" data-edit-acc="${core.escapeHtml(acc.id)}" type="button">🏷️ 编辑</button>
            </div>
          </td>
          <td style="text-align:right;white-space:nowrap;">
            <div style="display:inline-flex;gap:4px;">
              <button class="btn btn-xs" data-open-acc-inbox="${core.escapeHtml(acc.id)}" type="button">收件箱</button>
              <button class="btn btn-xs" data-sync-acc="${core.escapeHtml(acc.id)}" type="button">同步</button>
              <button class="btn btn-xs" data-copy-text="${core.escapeHtml(acc.mail_token)}" data-copy-label="MAIL_TOKEN" type="button">复制Token</button>
              <button class="btn btn-xs ${acc.status === 'expired' ? 'btn-warn' : ''}" data-reapply-acc="${core.escapeHtml(acc.id)}" type="button" title="当 MAIL_TOKEN 失效时重新向该上游申请新 Token">重申Token</button>
              <button class="btn btn-xs btn-danger" data-delete-acc="${core.escapeHtml(acc.id)}" type="button">删除</button>
            </div>
          </td>
        </tr>
      `;
    }).join('');
  }

  // Concurrency helper
  async function runWithConcurrency(items, concurrency, worker) {
    const limit = Math.max(1, Number(concurrency) || 3);
    let idx = 0;
    const runners = Array.from({ length: Math.min(limit, items.length) }, async () => {
      while (idx < items.length) {
        const current = items[idx++];
        await worker(current);
      }
    });
    await Promise.all(runners);
  }

  async function syncOneAccount(accountId, { silent = false } = {}) {
    const acc = state.accounts.find((a) => a.id === accountId);
    if (!acc) return;

    if (!acc.mail_token) {
      acc.status = 'expired';
      acc.dead_reason = '缺少 MAIL_TOKEN，请点击「重申Token」';
      await state.db.putAccount(acc);
      renderAll();
      return;
    }

    acc.status = 'fetching';
    state.runningJobs++;
    renderStatsAndSidebar();

    try {
      const data = await api.listMessages({ upstreamUrl: acc.upstream, mailToken: acc.mail_token });
      const summaries = Array.isArray(data && data.messages) ? data.messages : [];
      const existingByUid = new Map(state.emails.map((e) => [e.uid, e]));
      const toSave = [];

      for (const s of summaries) {
        const uid = `${acc.id}::${s._id}`;
        const prev = existingByUid.get(uid);
        if (prev && prev.body_html !== undefined) {
          continue;
        }

        let detail = null;
        try {
          detail = await api.getMessageDetail({
            upstreamUrl: acc.upstream,
            mailToken: acc.mail_token,
            id: s._id,
          });
        } catch (detailErr) {
          if (detailErr instanceof api.ApiError && detailErr.status === 401) {
            throw detailErr;
          }
        }

        const bodyHtml = detail && detail.bodyHtml !== undefined ? detail.bodyHtml : '';
        const cleanPreview = core.stripHtmlToText(bodyHtml || s.bodyPreview || '');
        const extractedCode = core.extractVerificationCode(s.subject, cleanPreview, bodyHtml);

        const emailObj = {
          uid,
          id: s._id,
          account_id: acc.id,
          mailbox: acc.mail,
          received_at: s.receivedAt || (detail && detail.receivedAt) || Math.floor(Date.now() / 1000),
          from: s.from || (detail && detail.from) || '',
          subject: s.subject || (detail && detail.subject) || '',
          body_preview: cleanPreview || s.bodyPreview || '',
          body_html: bodyHtml,
          attachments_count: s.attachmentsCount || 0,
          extracted_code: extractedCode,
          is_read: prev ? prev.is_read : false,
        };
        toSave.push(emailObj);
      }

      if (toSave.length > 0) {
        await state.db.putEmailsBulk(toSave);
        const byUid = new Map(state.emails.map((e) => [e.uid, e]));
        toSave.forEach((e) => byUid.set(e.uid, e));
        state.emails = Array.from(byUid.values());
      }

      const accEmails = state.emails.filter((e) => e.account_id === acc.id);
      acc.status = accEmails.length > 0 ? 'active' : 'empty';
      acc.dead_reason = '';
      acc.last_checked_at = Date.now();
      await state.db.putAccount(acc);

      if (!silent && toSave.length > 0) {
        showToast(`${acc.mail} 收到 ${toSave.length} 封新邮件`, 'ok');
      }
    } catch (err) {
      acc.last_checked_at = Date.now();
      if (err instanceof api.ApiError && err.status === 401) {
        acc.status = 'expired';
        acc.dead_reason = '401 MAIL_TOKEN 已失效或已过期';
        if (!silent) showToast(`${acc.mail} Token 已失效，可点击「重申Token」`, 'warn');
      } else {
        // Preserve previous non-fetching status on transient network failure
        const hasMail = state.emails.some((e) => e.account_id === acc.id);
        acc.status = hasMail ? 'active' : 'idle';
        acc.dead_reason = err && err.message ? err.message : '同步失败';
        if (!silent) showToast(`同步 ${acc.mail} 失败: ${acc.dead_reason}`, 'err');
      }
      await state.db.putAccount(acc);
    } finally {
      state.runningJobs = Math.max(0, state.runningJobs - 1);
      renderAll();
    }
  }

  async function syncManyAccounts(accountList, { silent = true } = {}) {
    if (!accountList || accountList.length === 0) return;
    await runWithConcurrency(accountList, state.settings.concurrency || 3, async (acc) => {
      await syncOneAccount(acc.id, { silent });
    });
  }

  /**
   * Re-apply MAIL_TOKEN for an account (or allocate replacement if upstream ignores explicit mailbox).
   */
  async function reapplyAccountToken(accountId, { silent = false } = {}) {
    const acc = state.accounts.find((a) => a.id === accountId);
    if (!acc) return { outcome: 'missing' };

    const tokenToUse = acc.token !== undefined && acc.token !== '' ? acc.token : (state.settings.createToken || '');
    try {
      const resp = await api.createMailbox({
        upstreamUrl: acc.upstream,
        createToken: tokenToUse,
        mailbox: acc.mail,
      });

      if (resp && resp.mailbox && resp.mailbox.toLowerCase() === acc.mail.toLowerCase()) {
        acc.mail_token = resp.token;
        acc.token = tokenToUse;
        acc.status = 'idle';
        acc.dead_reason = '';
        await state.db.putAccount(acc);
        if (!silent) showToast(`✓ ${acc.mail} Token 重申成功`, 'ok');
        await syncOneAccount(acc.id, { silent: true });
        return { outcome: 'renewed' };
      }

      // Fallback when upstream ignores explicit mailbox and allocates a new random address
      if (resp && resp.mailbox && resp.token) {
        const newAcc = {
          id: core.makeAccountId(acc.upstream, resp.mailbox),
          upstream: acc.upstream,
          token: tokenToUse,
          mail: resp.mailbox,
          mail_token: resp.token,
          mode: acc.mode || 'maindomain',
          tags: Array.isArray(acc.tags) ? [...acc.tags] : [],
          status: 'idle',
          dead_reason: '',
          unread_count: 0,
          latest_code: '',
          last_checked_at: null,
          created_at: Date.now(),
        };
        state.accounts.unshift(newAcc);
        await state.db.putAccount(newAcc);
        state.selectedAccountId = newAcc.id;
        renderAll();
        if (!silent) showToast(`上游已分配新邮箱 ${newAcc.mail}`, 'ok');
        return { outcome: 'replaced' };
      }
      return { outcome: 'error' };
    } catch (err) {
      if (err instanceof api.ApiError && err.status === 409) {
        if (!silent) showToast(`⏳ ${acc.mail} 仍在有效期内（尚未释放），不可重复申请`, 'warn');
        return { outcome: 'conflict' };
      }
      if (err instanceof api.ApiError && err.status === 403) {
        if (!silent) showToast(`🔒 ${acc.mail} 上游 CREATE_TOKEN 无效或缺失 (403)`, 'err');
        return { outcome: 'forbidden' };
      }
      if (!silent) showToast(`重申失败: ${err.message}`, 'err');
      return { outcome: 'error' };
    }
  }

  async function batchReapplyAccounts(targets) {
    if (!targets || targets.length === 0) {
      showToast('没有需要重申 Token 的失效邮箱', 'warn');
      return;
    }
    let renewed = 0;
    let conflict = 0;
    let failed = 0;
    await runWithConcurrency(targets, state.settings.concurrency || 3, async (acc) => {
      const res = await reapplyAccountToken(acc.id, { silent: true });
      if (res.outcome === 'renewed' || res.outcome === 'replaced') renewed++;
      else if (res.outcome === 'conflict') conflict++;
      else failed++;
    });
    renderAll();
    showToast(`批量重申完成：成功 ${renewed}，尚未释放跳过 ${conflict}，失败 ${failed}`, failed > 0 ? 'warn' : 'ok');
  }

  async function handleProbeUpstream() {
    const url = core.normalizeUpstreamUrl(el('inputUpstreamUrl').value);
    el('inputUpstreamUrl').value = url;
    const hint = el('probeResultHint');
    hint.textContent = `正在探测 ${url}/info ...`;
    try {
      const info = await api.fetchServerInfo(url);
      const serverDomains = Array.isArray(info && info.domains) ? info.domains : [];
      const currentParsed = core.parseDomainsInput(el('inputDomainsRaw').value);
      const merged = Array.from(new Set([...currentParsed, ...serverDomains]));
      if (merged.length > 0) {
        el('inputDomainsRaw').value = merged.join(', ');
        rebuildDomainStrategyOptions();
      }
      persistCreateFormToSettings();
      const tokenMsg = info && info.requiresCreateToken ? '服务端开启了 CREATE_TOKEN 鉴权' : '服务端无需 CREATE_TOKEN';
      hint.textContent = `✓ 连接成功！发现域名 [${serverDomains.join(', ')}] · ${tokenMsg}`;
      showToast('上游探测成功，已更新可用域名', 'ok');
    } catch (err) {
      hint.textContent = `探测失败 (${err.message})，旧版服务可能无 /info 接口，可手动填写域名直接申请`;
      showToast('未能自动获取 /info，可手动填写域名直接申请', 'warn');
    }
  }

  async function executeBatchCreate({ countOverride = null } = {}) {
    persistCreateFormToSettings();
    const upstreamUrl = state.settings.upstreamUrl;
    const createToken = state.settings.createToken;
    const domains = core.parseDomainsInput(state.settings.domainsRaw);
    const strategy = state.settings.domainStrategy;
    const mailMode = state.settings.mailMode;
    const count = countOverride || state.settings.batchCount || 1;
    const tags = (state.settings.batchTags || '').split(/[,，\s]+/).map((s) => s.trim()).filter(Boolean);

    const logBox = el('createProgressBox');
    logBox.classList.remove('hidden');
    logBox.textContent = `开始向 ${upstreamUrl} 申请 ${count} 个邮箱...\n`;

    const btnExec = el('btnExecuteCreate');
    btnExec.disabled = true;

    const createdAccounts = [];
    for (let i = 0; i < count; i++) {
      const { domain, nextCursor } = core.pickDomainForRequest({
        domains,
        strategy,
        cursor: state.settings.rotateCursor || 0,
      });
      state.settings.rotateCursor = nextCursor;
      const type = core.pickModeForRequest({ mode: mailMode, index: i });

      try {
        const resp = await api.createMailbox({
          upstreamUrl,
          createToken,
          domain,
          type,
        });

        const acc = {
          id: core.makeAccountId(upstreamUrl, resp.mailbox),
          upstream: upstreamUrl,
          token: createToken,
          mail: resp.mailbox,
          mail_token: resp.token,
          mode: type,
          tags,
          status: 'idle',
          dead_reason: '',
          unread_count: 0,
          latest_code: '',
          last_checked_at: null,
          created_at: Date.now(),
        };

        const idx = state.accounts.findIndex((a) => a.id === acc.id);
        if (idx >= 0) state.accounts[idx] = acc;
        else state.accounts.unshift(acc);
        await state.db.putAccount(acc);
        createdAccounts.push(acc);

        logBox.textContent += `[${i + 1}/${count}] ✓ (${type}${domain ? ` @ ${domain}` : ''}) -> ${acc.mail}\n`;
      } catch (err) {
        logBox.textContent += `[${i + 1}/${count}] ✕ 失败: ${err.message}\n`;
        if (err instanceof api.ApiError && err.status === 403) {
          showToast('创建 TOKEN (CREATE_TOKEN) 无效或未填写 (403)', 'err');
          break;
        }
      }
      logBox.scrollTop = logBox.scrollHeight;
    }

    saveCurrentSettings();
    btnExec.disabled = false;

    if (createdAccounts.length > 0) {
      state.selectedAccountId = createdAccounts[0].id;
      if (state.activeFolder === 'accounts') state.activeFolder = 'inbox';
      renderAll();
      showToast(`成功申请 ${createdAccounts.length} 个邮箱`, 'ok');
      syncManyAccounts(createdAccounts, { silent: true });
    } else {
      showToast('未成功创建邮箱，请检查上游地址、TOKEN 或 CORS 配置', 'err');
    }
  }

  // Import & Export
  function refreshImportPreview() {
    const text = el('inputImportText').value;
    const defaultUpstream = el('inputImportDefaultUpstream').value || state.settings.upstreamUrl;
    const defaultToken = el('inputImportDefaultToken').value;
    const res = core.parseJsonlImport(text, { defaultUpstream, defaultToken });
    state.parsedImport = res;

    el('importParseStat').textContent = `已解析 ${res.accounts.length} 个账号`;
    el('btnConfirmImport').textContent = `确认导入 (${res.accounts.length})`;
    el('btnConfirmImport').disabled = res.accounts.length === 0;
    el('importFooterHint').textContent = res.accounts.length > 0
      ? `准备导入 ${res.accounts.length} 个邮箱凭证`
      : '请上传 .jsonl 文件或粘贴内容';

    const errBox = el('importErrorsBox');
    if (res.errors.length > 0) {
      errBox.classList.remove('hidden');
      errBox.textContent = res.errors.slice(0, 10).join('\n');
    } else {
      errBox.classList.add('hidden');
      errBox.textContent = '';
    }

    const previewWrap = el('importPreviewWrap');
    const previewBody = el('importPreviewBody');
    if (res.accounts.length > 0) {
      previewWrap.classList.remove('hidden');
      previewBody.innerHTML = res.accounts.slice(0, 10).map((acc, i) => `
        <tr>
          <td class="mono">${i + 1}</td>
          <td class="mono">${core.escapeHtml(acc.mail)}</td>
          <td class="mono">${core.escapeHtml(acc.upstream)}</td>
          <td class="mono">${acc.token ? '已配置' : '空'}</td>
          <td class="mono">${acc.mail_token ? `${core.escapeHtml(acc.mail_token.slice(0, 12))}...` : '(无)'}</td>
        </tr>
      `).join('');
    } else {
      previewWrap.classList.add('hidden');
      previewBody.innerHTML = '';
    }
  }

  async function confirmImportAccounts() {
    const items = state.parsedImport.accounts;
    if (!items || items.length === 0) return;

    const strategy = el('selectImportDedup').value;
    const extraTags = el('inputImportTags').value.split(/[,，\s]+/).map((s) => s.trim()).filter(Boolean);
    const autoSync = el('chkImportAutoSync').checked;

    const existingMap = new Map(state.accounts.map((a) => [a.id, a]));
    const importedOrUpdated = [];

    for (const item of items) {
      const existing = existingMap.get(item.id);
      const mergedTags = Array.from(new Set([...(item.tags || []), ...extraTags]));

      if (existing) {
        if (strategy === 'skip') continue;
        const nextAcc = {
          ...existing,
          upstream: item.upstream,
          token: item.token,
          mail_token: item.mail_token || existing.mail_token,
          mode: item.mode || existing.mode,
          status: (item.mail_token || existing.mail_token) ? 'idle' : 'expired',
          dead_reason: '',
          tags: strategy === 'merge_tags'
            ? Array.from(new Set([...(existing.tags || []), ...mergedTags]))
            : mergedTags,
        };
        existingMap.set(nextAcc.id, nextAcc);
        importedOrUpdated.push(nextAcc);
      } else {
        const nextAcc = {
          ...item,
          tags: mergedTags,
          unread_count: 0,
          latest_code: '',
          last_checked_at: null,
        };
        existingMap.set(nextAcc.id, nextAcc);
        importedOrUpdated.push(nextAcc);
      }
    }

    state.accounts = Array.from(existingMap.values());
    await state.db.putAccountsBulk(importedOrUpdated);
    closeModal('importModal');
    renderAll();
    showToast(`成功导入 ${importedOrUpdated.length} 个邮箱`, 'ok');

    if (autoSync && importedOrUpdated.length > 0) {
      syncManyAccounts(importedOrUpdated, { silent: true });
    }
  }

  function buildExportData() {
    const scope = el('selectExportScope').value;
    const format = el('selectExportFormat').value;
    let target = state.accounts;
    if (scope === 'active') target = state.accounts.filter((a) => a.status === 'active');
    else if (scope === 'empty') target = state.accounts.filter((a) => a.status === 'empty' || a.status === 'idle');
    else if (scope === 'expired') target = state.accounts.filter((a) => a.status === 'expired');
    else if (scope === 'selected') target = state.accounts.filter((a) => state.selectedAccountIds.has(a.id));

    let content = '';
    if (target.length > 0) {
      if (format === 'jsonl') {
        content = core.serializeJsonl(target);
      } else if (format === 'json') {
        content = JSON.stringify(target.map((a) => ({
          upstream: a.upstream,
          token: a.token || '',
          mail: a.mail,
          mail_token: a.mail_token || '',
          mode: a.mode || 'maindomain',
          tags: a.tags || [],
          created_at: a.created_at || Date.now(),
        })), null, 2);
      } else if (format === 'delim') {
        content = target.map((a) => `${a.upstream}----${a.token || ''}----${a.mail}----${a.mail_token || ''}`).join('\n');
      } else {
        content = target.map((a) => a.mail).join('\n');
      }
    }

    const extMap = { jsonl: 'jsonl', json: 'json', delim: 'txt', mail_only: 'txt' };
    el('exportPreviewCount').textContent = `数据预览（${target.length} 条）：`;
    el('exportFormatLabel').textContent = format.toUpperCase();
    el('exportFooterCount').textContent = `共 ${target.length} 个凭据`;
    el('textareaExportPreview').value = content;
    el('btnCopyExport').disabled = target.length === 0;
    el('btnDownloadExport').disabled = target.length === 0;
    return { content, ext: extMap[format] || 'jsonl', scope };
  }

  function openModal(id) {
    if (id === 'createModal') {
      syncCreateModalFromSettings();
    } else if (id === 'importModal') {
      el('inputImportDefaultUpstream').value = state.settings.upstreamUrl || core.DEFAULT_UPSTREAM_URL;
      el('inputImportDefaultToken').value = state.settings.createToken || '';
      refreshImportPreview();
    } else if (id === 'exportModal') {
      buildExportData();
    }
    el(id).classList.remove('hidden');
  }

  function closeModal(id) {
    el(id).classList.add('hidden');
  }

  function openEditAccountModal(accountId) {
    const acc = state.accounts.find((a) => a.id === accountId);
    if (!acc) return;
    state.editingAccountId = acc.id;
    el('editAccMailTitle').textContent = acc.mail;
    el('editAccUpstream').value = acc.upstream;
    el('editAccToken').value = acc.token || '';
    el('editAccMailToken').value = acc.mail_token || '';
    el('editAccTags').value = (acc.tags || []).join(', ');
    openModal('editAccountModal');
  }

  async function saveAccountEdit({ reapplyAfter = false } = {}) {
    const acc = state.accounts.find((a) => a.id === state.editingAccountId);
    if (!acc) return;

    const newUpstream = core.normalizeUpstreamUrl(el('editAccUpstream').value);
    const newToken = el('editAccToken').value.trim();
    const newMailToken = el('editAccMailToken').value.trim();
    const newTags = el('editAccTags').value.split(/[,，\s]+/).map((s) => s.trim()).filter(Boolean);

    const oldId = acc.id;
    const newId = core.makeAccountId(newUpstream, acc.mail);
    acc.id = newId;
    acc.upstream = newUpstream;
    acc.token = newToken;
    acc.mail_token = newMailToken;
    acc.tags = newTags;
    if (newMailToken && acc.status === 'expired') {
      acc.status = 'idle';
      acc.dead_reason = '';
    }

    if (oldId !== newId) {
      await state.db.deleteAccount(oldId);
      state.emails.forEach((e) => {
        if (e.account_id === oldId) {
          e.account_id = newId;
          e.uid = `${newId}::${e.id}`;
        }
      });
      await state.db.putEmailsBulk(state.emails.filter((e) => e.account_id === newId));
      if (state.selectedAccountId === oldId) state.selectedAccountId = newId;
    }

    await state.db.putAccount(acc);
    closeModal('editAccountModal');
    renderAll();

    if (reapplyAfter) {
      await reapplyAccountToken(acc.id);
    } else {
      showToast('已保存邮箱配置', 'ok');
    }
  }

  function updatePollingTimer() {
    if (state.pollTimerId) {
      clearInterval(state.pollTimerId);
      state.pollTimerId = null;
    }
    if (state.isPolling) {
      const intervalMs = Math.max(5000, (Number(state.settings.pollInterval) || 30) * 1000);
      state.pollTimerId = setInterval(() => {
        const activeTargets = state.accounts.filter((a) => a.status !== 'expired');
        if (activeTargets.length > 0 && state.runningJobs === 0) {
          syncManyAccounts(activeTargets, { silent: true });
        }
      }, intervalMs);
    }
    renderStatsAndSidebar();
  }

  function bindEvents() {
    // Modal close buttons & backdrop click
    document.querySelectorAll('[data-close-modal]').forEach((btn) => {
      btn.addEventListener('click', () => closeModal(btn.dataset.closeModal));
    });
    document.querySelectorAll('.modal-backdrop').forEach((bd) => {
      bd.addEventListener('click', (e) => {
        if (e.target === bd) bd.classList.add('hidden');
      });
    });

    // Topbar & Sidebar triggers
    el('btnUpstreamChip').addEventListener('click', () => openModal('createModal'));
    el('btnTopCreate').addEventListener('click', () => openModal('createModal'));
    el('btnSidebarCreate').addEventListener('click', () => openModal('createModal'));
    el('btnTopImport').addEventListener('click', () => openModal('importModal'));
    el('btnSidebarImport').addEventListener('click', () => openModal('importModal'));
    el('btnTopExport').addEventListener('click', () => openModal('exportModal'));
    el('btnSidebarExport').addEventListener('click', () => openModal('exportModal'));

    el('btnToggleDark').addEventListener('click', () => {
      state.settings.darkMode = !state.settings.darkMode;
      applyDarkMode();
      saveCurrentSettings();
    });

    // Create modal interactions
    el('inputDomainsRaw').addEventListener('input', () => {
      rebuildDomainStrategyOptions();
    });
    el('selectDomainStrategy').addEventListener('change', () => {
      state.settings.domainStrategy = el('selectDomainStrategy').value;
      updateTopbarUpstreamChip();
    });
    el('btnProbeUpstream').addEventListener('click', handleProbeUpstream);
    el('btnClearDomains').addEventListener('click', () => {
      el('inputDomainsRaw').value = '';
      rebuildDomainStrategyOptions();
    });
    el('btnToggleTokenVis').addEventListener('click', () => {
      const inp = el('inputCreateToken');
      const isPwd = inp.type === 'password';
      inp.type = isPwd ? 'text' : 'password';
      el('btnToggleTokenVis').textContent = isPwd ? '隐藏' : '显示';
    });
    document.querySelectorAll('.quick-count').forEach((btn) => {
      btn.addEventListener('click', () => {
        el('inputBatchCount').value = btn.dataset.count;
      });
    });
    el('btnSaveUpstreamOnly').addEventListener('click', () => {
      persistCreateFormToSettings();
      showToast('上游与域名配置已保存', 'ok');
      renderAll();
    });
    el('btnExecuteCreate').addEventListener('click', () => executeBatchCreate());

    // Switcher dropdown
    el('btnSwitcherTrigger').addEventListener('click', (e) => {
      e.stopPropagation();
      state.switcherOpen = !state.switcherOpen;
      renderSwitcher();
      if (state.switcherOpen) el('switcherSearchInput').focus();
    });
    el('switcherSearchInput').addEventListener('input', (e) => {
      state.switcherSearch = e.target.value;
      renderSwitcher();
    });
    el('switcherList').addEventListener('click', (e) => {
      const btn = e.target.closest('[data-select-account]');
      if (!btn) return;
      const val = btn.dataset.selectAccount;
      state.selectedAccountId = val === '__all__' ? null : val;
      state.selectedEmailUid = null;
      if (state.activeFolder === 'accounts') state.activeFolder = 'inbox';
      state.switcherOpen = false;
      renderAll();
    });
    document.addEventListener('click', (e) => {
      if (state.switcherOpen && !e.target.closest('.switcher-box')) {
        state.switcherOpen = false;
        renderSwitcher();
      }
    });

    // Folders
    document.querySelectorAll('.nav-btn').forEach((btn) => {
      btn.addEventListener('click', () => {
        state.activeFolder = btn.dataset.folder;
        renderAll();
      });
    });

    // Polling controls
    el('btnTogglePolling').addEventListener('click', () => {
      state.isPolling = !state.isPolling;
      updatePollingTimer();
      showToast(state.isPolling ? '已开启自动定时刷新' : '已关闭自动定时刷新', 'ok');
    });
    el('rangeConcurrency').addEventListener('input', (e) => {
      state.settings.concurrency = Number(e.target.value) || 3;
      saveCurrentSettings();
      el('concurrencyVal').textContent = String(state.settings.concurrency);
    });
    el('inputPollInterval').addEventListener('change', (e) => {
      const v = Math.max(5, Math.min(3600, Number(e.target.value) || 30));
      state.settings.pollInterval = v;
      saveCurrentSettings();
      updatePollingTimer();
    });
    document.querySelectorAll('.preset-btn').forEach((btn) => {
      btn.addEventListener('click', () => {
        state.settings.pollInterval = Number(btn.dataset.preset) || 30;
        saveCurrentSettings();
        updatePollingTimer();
      });
    });

    // Batch actions in sidebar
    el('btnSyncAll').addEventListener('click', () => {
      const list = state.accounts.filter((a) => a.status !== 'expired');
      if (list.length === 0) {
        showToast('没有可同步的有效邮箱', 'warn');
        return;
      }
      syncManyAccounts(list, { silent: true }).then(() => showToast('全部邮箱同步完成', 'ok'));
    });
    el('btnReapplyAllExpired').addEventListener('click', () => {
      const expired = state.accounts.filter((a) => a.status === 'expired');
      batchReapplyAccounts(expired);
    });
    el('btnPurgeExpired').addEventListener('click', async () => {
      const expired = state.accounts.filter((a) => a.status === 'expired');
      if (expired.length === 0) return;
      const ids = new Set(expired.map((a) => a.id));
      state.accounts = state.accounts.filter((a) => !ids.has(a.id));
      state.emails = state.emails.filter((e) => !ids.has(e.account_id));
      if (state.selectedAccountId && ids.has(state.selectedAccountId)) state.selectedAccountId = null;
      await state.db.deleteAccountsBulk(Array.from(ids));
      renderAll();
      showToast(`已清理 ${expired.length} 个失效邮箱`, 'ok');
    });

    // Mail list interactions
    el('mailSearchInput').addEventListener('input', (e) => {
      state.mailSearch = e.target.value;
      renderMailList();
    });
    document.querySelectorAll('.subtab').forEach((btn) => {
      btn.addEventListener('click', () => {
        state.subFilter = btn.dataset.subfilter;
        renderMailList();
      });
    });
    el('btnRefreshCurrent').addEventListener('click', () => {
      if (state.selectedAccountId) {
        syncOneAccount(state.selectedAccountId);
      } else {
        syncManyAccounts(state.accounts.filter((a) => a.status !== 'expired'), { silent: true });
      }
    });
    el('mailListContainer').addEventListener('click', async (e) => {
      const codeBtn = e.target.closest('[data-copy-code]');
      if (codeBtn) {
        e.stopPropagation();
        copyText(codeBtn.dataset.copyCode, '验证码');
        return;
      }
      const card = e.target.closest('[data-select-email]');
      if (!card) return;
      const uid = card.dataset.selectEmail;
      state.selectedEmailUid = uid;
      const email = state.emails.find((m) => m.uid === uid);
      if (email && !email.is_read) {
        email.is_read = true;
        await state.db.putEmail(email);
      }
      renderAll();
    });

    // Mail display delegated actions
    el('mailDisplayContainer').addEventListener('click', async (e) => {
      if (e.target.id === 'btnWelcomeQuickApply') {
        syncCreateModalFromSettings();
        executeBatchCreate({ countOverride: 1 });
        return;
      }
      if (e.target.id === 'btnWelcomeConfigCreate') {
        openModal('createModal');
        return;
      }
      if (e.target.id === 'btnWelcomeImport') {
        openModal('importModal');
        return;
      }
      const copyCodeBtn = e.target.closest('[data-copy-code]');
      if (copyCodeBtn) {
        copyText(copyCodeBtn.dataset.copyCode, '验证码');
        return;
      }
      const viewBtn = e.target.closest('[data-mail-view]');
      if (viewBtn) {
        state.mailView = viewBtn.dataset.mailView;
        renderMailDisplay();
        return;
      }
      const toggleReadBtn = e.target.closest('[data-toggle-read]');
      if (toggleReadBtn) {
        const email = state.emails.find((m) => m.uid === toggleReadBtn.dataset.toggleRead);
        if (email) {
          email.is_read = !email.is_read;
          await state.db.putEmail(email);
          renderAll();
        }
        return;
      }
      const delEmailBtn = e.target.closest('[data-delete-email]');
      if (delEmailBtn) {
        const uid = delEmailBtn.dataset.deleteEmail;
        state.emails = state.emails.filter((m) => m.uid !== uid);
        if (state.selectedEmailUid === uid) state.selectedEmailUid = null;
        await state.db.deleteEmail(uid);
        renderAll();
        showToast('已删除本地邮件记录', 'ok');
        return;
      }
      const reapplyBtn = e.target.closest('[data-reapply-acc]');
      if (reapplyBtn) {
        reapplyAccountToken(reapplyBtn.dataset.reapplyAcc);
        return;
      }
      const editBtn = e.target.closest('[data-edit-acc]');
      if (editBtn) {
        openEditAccountModal(editBtn.dataset.editAcc);
      }
    });

    // AccountManager interactions
    el('accSearchInput').addEventListener('input', (e) => {
      state.accSearch = e.target.value;
      renderAccountManager();
    });
    el('accSortSelect').addEventListener('change', (e) => {
      state.accSort = e.target.value;
      renderAccountManager();
    });
    el('accStatusFilter').addEventListener('change', (e) => {
      state.accStatusFilter = e.target.value;
      renderAccountManager();
    });
    el('chkAllAccounts').addEventListener('change', (e) => {
      const list = getFilteredSortedAccounts();
      if (e.target.checked) list.forEach((a) => state.selectedAccountIds.add(a.id));
      else list.forEach((a) => state.selectedAccountIds.delete(a.id));
      renderAccountManager();
    });
    el('btnCopyFilteredMails').addEventListener('click', () => {
      const list = getFilteredSortedAccounts();
      if (list.length > 0) copyText(list.map((a) => a.mail).join('\n'), `全部 ${list.length} 个邮箱`);
    });
    el('btnSyncSelected').addEventListener('click', () => {
      const selected = state.accounts.filter((a) => state.selectedAccountIds.has(a.id));
      const targets = selected.length > 0 ? selected : getFilteredSortedAccounts();
      syncManyAccounts(targets, { silent: true }).then(() => showToast(`已同步 ${targets.length} 个邮箱`, 'ok'));
    });
    el('btnReapplySelected').addEventListener('click', () => {
      const selected = state.accounts.filter((a) => state.selectedAccountIds.has(a.id));
      const targets = selected.length > 0 ? selected : state.accounts.filter((a) => a.status === 'expired');
      batchReapplyAccounts(targets);
    });

    el('accTableBody').addEventListener('change', (e) => {
      const chk = e.target.closest('[data-acc-chk]');
      if (!chk) return;
      const id = chk.dataset.accChk;
      if (chk.checked) state.selectedAccountIds.add(id);
      else state.selectedAccountIds.delete(id);
      renderAccountManager();
    });

    el('accTableBody').addEventListener('click', async (e) => {
      const copyCode = e.target.closest('[data-copy-code]');
      if (copyCode) {
        copyText(copyCode.dataset.copyCode, '验证码');
        return;
      }
      const copyBtn = e.target.closest('[data-copy-text]');
      if (copyBtn) {
        copyText(copyBtn.dataset.copyText, copyBtn.dataset.copyLabel || '内容');
        return;
      }
      const openBtn = e.target.closest('[data-open-acc-inbox]');
      if (openBtn) {
        state.selectedAccountId = openBtn.dataset.openAccInbox;
        state.selectedEmailUid = null;
        state.activeFolder = 'inbox';
        renderAll();
        return;
      }
      const syncBtn = e.target.closest('[data-sync-acc]');
      if (syncBtn) {
        syncOneAccount(syncBtn.dataset.syncAcc);
        return;
      }
      const reapplyBtn = e.target.closest('[data-reapply-acc]');
      if (reapplyBtn) {
        reapplyAccountToken(reapplyBtn.dataset.reapplyAcc);
        return;
      }
      const editBtn = e.target.closest('[data-edit-acc]');
      if (editBtn) {
        openEditAccountModal(editBtn.dataset.editAcc);
        return;
      }
      const delBtn = e.target.closest('[data-delete-acc]');
      if (delBtn) {
        const id = delBtn.dataset.deleteAcc;
        state.accounts = state.accounts.filter((a) => a.id !== id);
        state.emails = state.emails.filter((m) => m.account_id !== id);
        state.selectedAccountIds.delete(id);
        if (state.selectedAccountId === id) state.selectedAccountId = null;
        await state.db.deleteAccount(id);
        renderAll();
        showToast('已删除邮箱及其本地缓存邮件', 'ok');
      }
    });

    // Import modal interactions
    const dz = el('importDropzone');
    const fileInput = el('inputImportFile');
    const readFileIntoImport = (file) => {
      if (!file) return;
      const reader = new FileReader();
      reader.onload = () => {
        el('inputImportText').value = String(reader.result || '');
        refreshImportPreview();
      };
      reader.readAsText(file, 'utf-8');
    };
    dz.addEventListener('click', () => fileInput.click());
    fileInput.addEventListener('change', (e) => readFileIntoImport(e.target.files[0]));
    dz.addEventListener('dragover', (e) => {
      e.preventDefault();
      dz.classList.add('dragover');
    });
    dz.addEventListener('dragleave', () => dz.classList.remove('dragover'));
    dz.addEventListener('drop', (e) => {
      e.preventDefault();
      dz.classList.remove('dragover');
      if (e.dataTransfer && e.dataTransfer.files[0]) {
        readFileIntoImport(e.dataTransfer.files[0]);
      }
    });
    ['inputImportText', 'inputImportDefaultUpstream', 'inputImportDefaultToken'].forEach((id) => {
      el(id).addEventListener('input', refreshImportPreview);
    });
    el('btnConfirmImport').addEventListener('click', confirmImportAccounts);

    // Export modal interactions
    el('selectExportScope').addEventListener('change', buildExportData);
    el('selectExportFormat').addEventListener('change', buildExportData);
    el('btnCopyExport').addEventListener('click', () => {
      const { content } = buildExportData();
      if (content) copyText(content, '导出数据');
    });
    el('btnDownloadExport').addEventListener('click', () => {
      const { content, ext, scope } = buildExportData();
      if (!content) return;
      const dateStr = new Date().toISOString().slice(0, 10);
      const blob = new Blob([content], { type: 'text/plain;charset=utf-8' });
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = `tempmail_accounts_${scope}_${dateStr}.${ext}`;
      document.body.appendChild(a);
      a.click();
      a.remove();
      URL.revokeObjectURL(url);
      showToast(`已下载 ${a.download}`, 'ok');
    });

    // Edit Account modal interactions
    el('btnSaveAccountEdit').addEventListener('click', () => saveAccountEdit({ reapplyAfter: false }));
    el('btnReapplyFromEdit').addEventListener('click', () => saveAccountEdit({ reapplyAfter: true }));
  }

  async function bootstrap() {
    applyDarkMode();
    await state.db.init();
    const [accounts, emails] = await Promise.all([
      state.db.getAllAccounts(),
      state.db.getAllEmails(),
    ]);
    state.accounts = accounts;
    state.emails = emails;

    syncCreateModalFromSettings();
    bindEvents();
    renderAll();
  }

  document.addEventListener('DOMContentLoaded', bootstrap);
})(typeof globalThis !== 'undefined' ? globalThis : window);
