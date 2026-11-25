(function (root) {
  'use strict';

  const core = root.TiCore;
  const SETTINGS_KEY = 'ti_tempmail_settings_v1';
  const DB_NAME = 'TiTempMailWorkbenchDB';
  const DB_VERSION = 1;

  function defaultSettings() {
    return { ...core.DEFAULT_SETTINGS };
  }

  function loadSettings() {
    try {
      const raw = localStorage.getItem(SETTINGS_KEY);
      if (!raw) return defaultSettings();
      return core.normalizeSettings(JSON.parse(raw), core.DEFAULT_SETTINGS);
    } catch {
      return defaultSettings();
    }
  }

  function saveSettings(settings) {
    try {
      localStorage.setItem(SETTINGS_KEY, JSON.stringify(settings));
    } catch {
      // Ignore storage quota / restricted context errors.
    }
  }

  function openDatabase() {
    return new Promise((resolve, reject) => {
      if (typeof indexedDB === 'undefined') {
        reject(new Error('IndexedDB unavailable'));
        return;
      }
      const req = indexedDB.open(DB_NAME, DB_VERSION);
      req.onupgradeneeded = (event) => {
        const db = event.target.result;
        if (!db.objectStoreNames.contains('accounts')) {
          const accStore = db.createObjectStore('accounts', { keyPath: 'id' });
          accStore.createIndex('mail', 'mail', { unique: false });
          accStore.createIndex('created_at', 'created_at', { unique: false });
        }
        if (!db.objectStoreNames.contains('emails')) {
          const emailStore = db.createObjectStore('emails', { keyPath: 'uid' });
          emailStore.createIndex('account_id', 'account_id', { unique: false });
          emailStore.createIndex('received_at', 'received_at', { unique: false });
        }
      };
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error || new Error('Failed to open IndexedDB'));
    });
  }

  function reqToPromise(req) {
    return new Promise((resolve, reject) => {
      req.onsuccess = () => resolve(req.result);
      req.onerror = () => reject(req.error);
    });
  }

  function txDone(tx) {
    return new Promise((resolve, reject) => {
      tx.oncomplete = () => resolve();
      tx.onerror = () => reject(tx.error || new Error('Transaction failed'));
      tx.onabort = () => reject(tx.error || new Error('Transaction aborted'));
    });
  }

  /**
   * Browser persistence manager backed by IndexedDB with automatic in-memory fallback.
   */
  class BrowserMailStore {
    constructor() {
      this.db = null;
      this.memAccounts = new Map();
      this.memEmails = new Map();
      this.usingFallback = false;
    }

    async init() {
      try {
        this.db = await openDatabase();
      } catch {
        this.usingFallback = true;
      }
    }

    async getAllAccounts() {
      if (this.usingFallback || !this.db) {
        return Array.from(this.memAccounts.values());
      }
      const tx = this.db.transaction('accounts', 'readonly');
      const store = tx.objectStore('accounts');
      return await reqToPromise(store.getAll());
    }

    async putAccount(acc) {
      if (this.usingFallback || !this.db) {
        this.memAccounts.set(acc.id, { ...acc });
        return;
      }
      const tx = this.db.transaction('accounts', 'readwrite');
      tx.objectStore('accounts').put({ ...acc });
      await txDone(tx);
    }

    async putAccountsBulk(list) {
      if (!list || list.length === 0) return;
      if (this.usingFallback || !this.db) {
        list.forEach((acc) => this.memAccounts.set(acc.id, { ...acc }));
        return;
      }
      const tx = this.db.transaction('accounts', 'readwrite');
      const store = tx.objectStore('accounts');
      list.forEach((acc) => store.put({ ...acc }));
      await txDone(tx);
    }

    async deleteAccount(accountId) {
      if (this.usingFallback || !this.db) {
        this.memAccounts.delete(accountId);
        for (const [uid, email] of this.memEmails.entries()) {
          if (email.account_id === accountId) this.memEmails.delete(uid);
        }
        return;
      }
      const tx = this.db.transaction(['accounts', 'emails'], 'readwrite');
      tx.objectStore('accounts').delete(accountId);
      const emailStore = tx.objectStore('emails');
      const idx = emailStore.index('account_id');
      const keys = await reqToPromise(idx.getAllKeys(accountId));
      for (const k of keys) {
        emailStore.delete(k);
      }
      await txDone(tx);
    }

    async deleteAccountsBulk(accountIds) {
      for (const id of accountIds) {
        await this.deleteAccount(id);
      }
    }

    async getAllEmails() {
      if (this.usingFallback || !this.db) {
        return Array.from(this.memEmails.values());
      }
      const tx = this.db.transaction('emails', 'readonly');
      return await reqToPromise(tx.objectStore('emails').getAll());
    }

    async putEmail(email) {
      if (this.usingFallback || !this.db) {
        this.memEmails.set(email.uid, { ...email });
        return;
      }
      const tx = this.db.transaction('emails', 'readwrite');
      tx.objectStore('emails').put({ ...email });
      await txDone(tx);
    }

    async putEmailsBulk(list) {
      if (!list || list.length === 0) return;
      if (this.usingFallback || !this.db) {
        list.forEach((e) => this.memEmails.set(e.uid, { ...e }));
        return;
      }
      const tx = this.db.transaction('emails', 'readwrite');
      const store = tx.objectStore('emails');
      list.forEach((e) => store.put({ ...e }));
      await txDone(tx);
    }

    async deleteEmail(uid) {
      if (this.usingFallback || !this.db) {
        this.memEmails.delete(uid);
        return;
      }
      const tx = this.db.transaction('emails', 'readwrite');
      tx.objectStore('emails').delete(uid);
      await txDone(tx);
    }

    async deleteEmailsBulk(uids) {
      if (!uids || uids.length === 0) return;
      if (this.usingFallback || !this.db) {
        uids.forEach((uid) => this.memEmails.delete(uid));
        return;
      }
      const tx = this.db.transaction('emails', 'readwrite');
      const store = tx.objectStore('emails');
      uids.forEach((uid) => store.delete(uid));
      await txDone(tx);
    }

    async clearAllEmails() {
      if (this.usingFallback || !this.db) {
        this.memEmails.clear();
        return;
      }
      const tx = this.db.transaction('emails', 'readwrite');
      tx.objectStore('emails').clear();
      await txDone(tx);
    }
  }

  root.TiStorage = {
    loadSettings,
    saveSettings,
    BrowserMailStore,
  };
})(typeof globalThis !== 'undefined' ? globalThis : window);
