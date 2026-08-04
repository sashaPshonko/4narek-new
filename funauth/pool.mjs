import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { randomUUID } from 'node:crypto';
import { TelegramClient, Api } from 'telegram';
import { StringSession } from 'telegram/sessions/index.js';
import { computeCheck } from 'telegram/Password.js';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const SESSIONS_DIR = path.join(__dirname, 'sessions');

function loadEnvCredentials() {
  const apiId = Number(process.env.TELEGRAM_API_ID);
  const apiHash = process.env.TELEGRAM_API_HASH || '';
  if (!apiId || !apiHash) {
    throw new Error('TELEGRAM_API_ID and TELEGRAM_API_HASH are required');
  }
  return { apiId, apiHash };
}

function ensureSessionsDir() {
  fs.mkdirSync(SESSIONS_DIR, { recursive: true });
}

function accountPath(id) {
  return path.join(SESSIONS_DIR, `${id}.json`);
}

function normalizePhone(phone) {
  return String(phone || '').replace(/[^\d+]/g, '');
}

/**
 * @typedef {{
 *   id: string,
 *   phone: string,
 *   username: string|null,
 *   session: string,
 *   ready: boolean,
 *   full: boolean,
 *   started: boolean,
 * }} AccountMeta
 */

export class AccountPool {
  constructor() {
    ensureSessionsDir();
    /** @type {Map<string, AccountMeta>} */
    this.accounts = new Map();
    /** @type {Map<string, TelegramClient>} */
    this.clients = new Map();
    /** @type {Map<string, { phone: string, phoneCodeHash: string, client: TelegramClient }>} */
    this.pending = new Map();
    const creds = loadEnvCredentials();
    this.apiId = creds.apiId;
    this.apiHash = creds.apiHash;
  }

  async init() {
    const files = fs.readdirSync(SESSIONS_DIR).filter((f) => f.endsWith('.json'));
    for (const file of files) {
      try {
        const raw = JSON.parse(fs.readFileSync(path.join(SESSIONS_DIR, file), 'utf8'));
        if (!raw?.id || !raw?.session) continue;
        /** @type {AccountMeta} */
        const meta = {
          id: raw.id,
          phone: raw.phone || '',
          username: raw.username || null,
          session: raw.session,
          ready: false,
          full: Boolean(raw.full),
          started: Boolean(raw.started),
        };
        this.accounts.set(meta.id, meta);
        await this._connectAccount(meta);
      } catch (err) {
        console.error(`[pool] failed to load ${file}:`, err.message);
      }
    }
    console.log(`[pool] loaded ${this.accounts.size} account(s)`);
  }

  list() {
    return [...this.accounts.values()].map((a) => ({
      id: a.id,
      phone: a.phone,
      username: a.username,
      ready: a.ready,
      full: a.full,
      started: a.started,
    }));
  }

  get(id) {
    return this.accounts.get(id) || null;
  }

  getClient(id) {
    return this.clients.get(id) || null;
  }

  /**
   * Pick a ready TG account that is not marked full.
   * @param {Set<string>} [exclude]
   */
  pickReady(exclude) {
    for (const a of this.accounts.values()) {
      if (exclude?.has(a.id)) continue;
      if (a.ready && !a.full && this.clients.has(a.id)) return a;
    }
    return null;
  }

  save(meta) {
    const data = {
      id: meta.id,
      phone: meta.phone,
      username: meta.username,
      session: meta.session,
      full: meta.full,
      started: meta.started,
    };
    fs.writeFileSync(accountPath(meta.id), JSON.stringify(data, null, 2));
    this.accounts.set(meta.id, meta);
  }

  markFull(id) {
    const a = this.accounts.get(id);
    if (!a) return;
    a.full = true;
    this.save(a);
  }

  markStarted(id) {
    const a = this.accounts.get(id);
    if (!a) return;
    a.started = true;
    this.save(a);
  }

  async _connectAccount(meta) {
    const client = new TelegramClient(
      new StringSession(meta.session),
      this.apiId,
      this.apiHash,
      { connectionRetries: 5, useWSS: true },
    );
    await client.connect();
    if (!(await client.isUserAuthorized())) {
      console.warn(`[pool] session not authorized for ${meta.phone || meta.id}`);
      await client.disconnect();
      meta.ready = false;
      this.accounts.set(meta.id, meta);
      return null;
    }
    try {
      const me = await client.getMe();
      meta.username = me?.username || null;
      if (!meta.phone && me?.phone) meta.phone = `+${me.phone}`;
    } catch {
      /* ignore */
    }
    meta.ready = true;
    this.clients.set(meta.id, client);
    this.save(meta);
    console.log(`[pool] connected ${meta.phone || meta.id}`);
    return client;
  }

  async loginStart(phone) {
    const normalized = normalizePhone(phone);
    if (!normalized) throw new Error('phone_required');

    // Drop previous pending for same phone
    const prev = this.pending.get(normalized);
    if (prev) {
      try { await prev.client.disconnect(); } catch { /* ignore */ }
      this.pending.delete(normalized);
    }

    const client = new TelegramClient(
      new StringSession(''),
      this.apiId,
      this.apiHash,
      { connectionRetries: 5, useWSS: true },
    );
    await client.connect();
    const result = await client.sendCode(
      { apiId: this.apiId, apiHash: this.apiHash },
      normalized,
    );
    this.pending.set(normalized, {
      phone: normalized,
      phoneCodeHash: result.phoneCodeHash,
      client,
    });
    return { phone: normalized, sent: true };
  }

  async loginCode(phone, code) {
    const normalized = normalizePhone(phone);
    const pending = this.pending.get(normalized);
    if (!pending) throw new Error('login_not_started');
    const codeStr = String(code || '').trim();
    if (!codeStr) throw new Error('code_required');

    try {
      await pending.client.invoke(
        new Api.auth.SignIn({
          phoneNumber: normalized,
          phoneCodeHash: pending.phoneCodeHash,
          phoneCode: codeStr,
        }),
      );
    } catch (err) {
      const msg = err?.errorMessage || err?.message || '';
      if (msg === 'SESSION_PASSWORD_NEEDED' || /PASSWORD/i.test(msg)) {
        return { phone: normalized, needPassword: true };
      }
      throw err;
    }

    return this._finalizeLogin(normalized);
  }

  async loginPassword(phone, password) {
    const normalized = normalizePhone(phone);
    const pending = this.pending.get(normalized);
    if (!pending) throw new Error('login_not_started');
    const pwd = String(password || '');
    if (!pwd) throw new Error('password_required');

    const pwdInfo = await pending.client.invoke(new Api.account.GetPassword());
    const passwordCheck = await computeCheck(pwdInfo, pwd);
    await pending.client.invoke(new Api.auth.CheckPassword({ password: passwordCheck }));

    return this._finalizeLogin(normalized);
  }

  async _finalizeLogin(phone) {
    const pending = this.pending.get(phone);
    if (!pending) throw new Error('login_not_started');

    const session = pending.client.session.save();
    let username = null;
    try {
      const me = await pending.client.getMe();
      username = me?.username || null;
    } catch {
      /* ignore */
    }

    // Replace existing account with same phone
    for (const [id, a] of this.accounts) {
      if (normalizePhone(a.phone) === phone) {
        await this.remove(id);
        break;
      }
    }

    const id = randomUUID();
    /** @type {AccountMeta} */
    const meta = {
      id,
      phone,
      username,
      session,
      ready: true,
      full: false,
      started: false,
    };
    this.save(meta);
    this.clients.set(id, pending.client);
    this.pending.delete(phone);

    return {
      id: meta.id,
      phone: meta.phone,
      username: meta.username,
      ready: true,
      full: false,
      started: false,
    };
  }

  async remove(id) {
    const client = this.clients.get(id);
    if (client) {
      try { await client.disconnect(); } catch { /* ignore */ }
      this.clients.delete(id);
    }
    this.accounts.delete(id);
    const p = accountPath(id);
    if (fs.existsSync(p)) fs.unlinkSync(p);
    return true;
  }

  async shutdown() {
    for (const client of this.clients.values()) {
      try { await client.disconnect(); } catch { /* ignore */ }
    }
    for (const p of this.pending.values()) {
      try { await p.client.disconnect(); } catch { /* ignore */ }
    }
    this.clients.clear();
    this.pending.clear();
  }
}
