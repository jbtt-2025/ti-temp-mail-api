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

  const jsonl = core.serializeJsonl(sample);
  const lines = jsonl.split('\n');
  assert.equal(lines.length, 2);

  const parsed = core.parseJsonlImport(jsonl);
  assert.equal(parsed.errors.length, 0);
  assert.equal(parsed.accounts.length, 2);
  assert.equal(parsed.accounts[0].upstream, 'https://anymail.77669876.xyz');
  assert.equal(parsed.accounts[0].token, 'create-secret');
  assert.equal(parsed.accounts[0].mail, 'alpha@77669876.xyz');
  assert.equal(parsed.accounts[0].mail_token, 'tok-alpha');
  assert.equal(parsed.accounts[1].upstream, 'http://127.0.0.1:8080');
  assert.equal(parsed.accounts[1].token, '');
  assert.equal(parsed.accounts[1].mail_token, 'tok-beta');

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
