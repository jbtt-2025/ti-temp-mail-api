'use strict';

const test = require('node:test');
const assert = require('node:assert/strict');
const core = require('./core.js');

test('default upstream URL and normalization', () => {
  assert.equal(core.DEFAULT_UPSTREAM_URL, 'https://anymail.77669876.xyz');
  assert.equal(core.normalizeUpstreamUrl(''), 'https://anymail.77669876.xyz');
  assert.equal(core.normalizeUpstreamUrl('anymail.77669876.xyz/'), 'https://anymail.77669876.xyz');
  assert.equal(core.normalizeUpstreamUrl('http://localhost:8080///'), 'http://localhost:8080');
});

test('parseDomainsInput supports multiple domains and cleanup', () => {
  const domains = core.parseDomainsInput('77669876.xyz, https://mail.Example.COM/\na.example.org; 77669876.xyz');
  assert.deepEqual(domains, ['77669876.xyz', 'mail.example.com', 'a.example.org']);
});

test('pickDomainForRequest supports rotate, random, server, and exact domain', () => {
  const domains = ['d1.com', 'd2.com', 'd3.com'];

  const r0 = core.pickDomainForRequest({ domains, strategy: 'rotate', cursor: 0 });
  const r1 = core.pickDomainForRequest({ domains, strategy: 'rotate', cursor: r0.nextCursor });
  const r2 = core.pickDomainForRequest({ domains, strategy: 'rotate', cursor: r1.nextCursor });
  const r3 = core.pickDomainForRequest({ domains, strategy: 'rotate', cursor: r2.nextCursor });
  assert.deepEqual([r0.domain, r1.domain, r2.domain, r3.domain], ['d1.com', 'd2.com', 'd3.com', 'd1.com']);

  const rand = core.pickDomainForRequest({ domains, strategy: 'random', rng: () => 0.5 });
  assert.equal(rand.domain, 'd2.com');

  const srv = core.pickDomainForRequest({ domains, strategy: 'server' });
  assert.equal(srv.domain, undefined);

  const exact = core.pickDomainForRequest({ domains, strategy: 'exact:custom.net' });
  assert.equal(exact.domain, 'custom.net');
});

test('pickModeForRequest supports maindomain, subdomain, and alternate', () => {
  assert.equal(core.pickModeForRequest({ mode: 'maindomain', index: 1 }), 'maindomain');
  assert.equal(core.pickModeForRequest({ mode: 'subdomain', index: 0 }), 'subdomain');
  assert.equal(core.pickModeForRequest({ mode: 'alternate', index: 0 }), 'maindomain');
  assert.equal(core.pickModeForRequest({ mode: 'alternate', index: 1 }), 'subdomain');
});

test('serializeJsonl and parseJsonlImport round-trip upstream + token + mail + mail_token', () => {
  const sample = [
    {
      upstream: 'https://anymail.77669876.xyz',
      token: 'create-secret',
      mail: 'alpha@77669876.xyz',
      mail_token: 'tok-alpha',
      mode: 'maindomain',
      tags: ['batch1'],
      created_at: 1700000000000,
    },
    {
      upstream: 'http://127.0.0.1:8080',
      token: '',
      mail: 'beta@sub.example.com',
      mail_token: 'tok-beta',
      mode: 'subdomain',
      tags: [],
      created_at: 1700000001000,
    },
  ];

  // Legacy round-trip without settings
  const jsonl = core.serializeJsonl(sample);
  const lines = jsonl.split('\n');
  assert.equal(lines.length, 2);

  const parsed = core.parseJsonlImport(jsonl);
  assert.equal(parsed.errors.length, 0);
  assert.equal(parsed.settings, null);
  assert.equal(parsed.accounts.length, 2);
  assert.equal(parsed.accounts[0].upstream, 'https://anymail.77669876.xyz');
  assert.equal(parsed.accounts[0].token, 'create-secret');
  assert.equal(parsed.accounts[0].mail, 'alpha@77669876.xyz');
  assert.equal(parsed.accounts[0].mail_token, 'tok-alpha');
  assert.equal(parsed.accounts[1].upstream, 'http://127.0.0.1:8080');
  assert.equal(parsed.accounts[1].token, '');
  assert.equal(parsed.accounts[1].mail_token, 'tok-beta');

  // Round-trip including frontend settings in JSONL & JSON bundle (emails ignored)
  const settings = {
    upstreamUrl: 'https://custom.example.com/',
    createToken: 'top-secret',
    domainsRaw: 'd1.example.com, d2.example.com',
    domainStrategy: 'exact:d2.example.com',
    mailMode: 'alternate',
    batchCount: 5,
    batchTags: 'vip',
    rotateCursor: 1,
    pollInterval: 60,
    concurrency: 4,
    darkMode: true,
    emailRetention: '7d',
  };

  const jsonlWithSettings = core.serializeExportData({
    accounts: sample,
    settings,
    includeSettings: true,
    format: 'jsonl',
  });
  const parsedJsonl = core.parseJsonlImport(jsonlWithSettings);
  assert.equal(parsedJsonl.errors.length, 0);
  assert.equal(parsedJsonl.accounts.length, 2);
  assert.equal(parsedJsonl.settings.upstreamUrl, 'https://custom.example.com');
  assert.equal(parsedJsonl.settings.domainsRaw, 'd1.example.com, d2.example.com');
  assert.equal(parsedJsonl.settings.domainStrategy, 'exact:d2.example.com');
  assert.equal(parsedJsonl.settings.mailMode, 'alternate');
  assert.equal(parsedJsonl.settings.emailRetention, '7d');
  assert.equal(parsedJsonl.settings.darkMode, true);

  // Settings-only export/import works even with zero accounts, and ignores emails array if present
  const jsonBundle = core.serializeExportData({
    accounts: [],
    settings,
    includeSettings: true,
    format: 'json',
  });
  const bundleWithNoise = JSON.parse(jsonBundle);
  bundleWithNoise.emails = [{ uid: 'ignored', body_html: '<p>ignore me</p>' }];
  const parsedBundle = core.parseJsonlImport(JSON.stringify(bundleWithNoise));
  assert.equal(parsedBundle.errors.length, 0);
  assert.equal(parsedBundle.accounts.length, 0);
  assert.equal(parsedBundle.settings.createToken, 'top-secret');

  // Alias keys & fallback upstream/token & error reporting
  const mixed = [
    '{"email":"gamma@example.com","mailToken":"tok-gamma"}',
    'https://u.test----tk----delta@u.test----mtk',
    '{bad-json',
  ].join('\n');
  const res2 = core.parseJsonlImport(mixed, {
    defaultUpstream: 'https://anymail.77669876.xyz/',
    defaultToken: 'fallback-tok',
  });
  assert.equal(res2.accounts.length, 2);
  assert.equal(res2.errors.length, 1);
  assert.equal(res2.accounts[0].upstream, 'https://anymail.77669876.xyz');
  assert.equal(res2.accounts[0].token, 'fallback-tok');
  assert.equal(res2.accounts[1].mail, 'delta@u.test');
  assert.equal(res2.accounts[1].mail_token, 'mtk');
});

test('partitionEmailsForCleanup filters emails by age or clears all', () => {
  const nowMs = Date.UTC(2026, 10, 25, 12, 0, 0);
  const nowSec = Math.floor(nowMs / 1000);
  const emails = [
    { uid: 'e-30m', received_at: nowSec - 30 * 60 },
    { uid: 'e-2h', received_at: nowSec - 2 * 3600 },
    { uid: 'e-4d', received_at: nowSec - 4 * 86400 },
    { uid: 'e-10d', received_at: (nowMs - 10 * 86400 * 1000) },
  ];

  const r1h = core.partitionEmailsForCleanup(emails, '1h', nowMs);
  assert.deepEqual(r1h.kept.map((e) => e.uid), ['e-30m']);
  assert.deepEqual(r1h.removedUids, ['e-2h', 'e-4d', 'e-10d']);

  const r7d = core.partitionEmailsForCleanup(emails, '7d', nowMs);
  assert.deepEqual(r7d.kept.map((e) => e.uid), ['e-30m', 'e-2h', 'e-4d']);
  assert.deepEqual(r7d.removedUids, ['e-10d']);

  const rAll = core.partitionEmailsForCleanup(emails, 'all', nowMs);
  assert.equal(rAll.kept.length, 0);
  assert.equal(rAll.removedUids.length, 4);
});

test('extractVerificationCode extracts OTP without picking year numbers', () => {
  assert.equal(core.extractVerificationCode('登录验证码', '您的验证码是：839201，5分钟内有效', ''), '839201');
  assert.equal(core.extractVerificationCode('Copyright 2026', '', '<div style="font-size:24px">741852</div>'), '741852');
  assert.equal(core.extractVerificationCode('News 2026', 'Welcome in 2026', '<p>Hello world</p>'), '');
});

test('sanitizeEmailHtml removes script tags, event handlers, and javascript URIs', () => {
  const dirty = '<div onclick="alert(1)"><script>steal()</script><a href="javascript:alert(2)">click</a><p>Safe</p></div>';
  const clean = core.sanitizeEmailHtml(dirty);
  assert.ok(!clean.includes('<script>'));
  assert.ok(!clean.includes('onclick'));
  assert.ok(!clean.includes('javascript:'));
  assert.ok(clean.includes('<p>Safe</p>'));
});
