import { Api } from 'telegram';
import { NewMessage } from 'telegram/events/index.js';

const FUNAUTH_BOT = 'FunAuthBot';
const FUNTIME_CHANNEL = 'funtime';
const BIND_REPLY_TIMEOUT_MS = 45_000;
const JOB_TIMEOUT_MS = 120_000;

const BIND_OK = /был привязан/i;
const BIND_FULL = /уже много привязанных|много привязанных/i;
const TWOFA_OK = /выключено|Подтверждение входа/i;

/**
 * @typedef {{
 *   nick: string,
 *   password: string,
 *   resolve: (v: any) => void,
 *   reject: (e: Error) => void,
 *   enqueuedAt: number,
 *   cancelled?: boolean,
 * }} BindJob
 */

export class Binder {
  /**
   * @param {import('./pool.mjs').AccountPool} pool
   */
  constructor(pool) {
    this.pool = pool;
    /** @type {BindJob[]} */
    this.queue = [];
    this.running = false;
    this.current = null;
  }

  status() {
    return {
      running: this.running,
      current: this.current
        ? { nick: this.current.nick, enqueuedAt: this.current.enqueuedAt }
        : null,
      pending: this.queue.map((j) => ({
        nick: j.nick,
        enqueuedAt: j.enqueuedAt,
      })),
      length: this.queue.length + (this.running ? 1 : 0),
    };
  }

  /**
   * Enqueue bind and wait for result (timeout ~120s).
   * @param {string} nick
   * @param {string} password
   */
  bind(nick, password) {
    const n = String(nick || '').trim();
    const p = String(password || '');
    if (!n || !p) {
      return Promise.resolve({ ok: false, nick: n, error: 'nick_password_required' });
    }

    return new Promise((resolve) => {
      let settled = false;
      const done = (result) => {
        if (settled) return;
        settled = true;
        resolve(result);
      };

      /** @type {BindJob} */
      const job = {
        nick: n,
        password: p,
        resolve: done,
        reject: (e) => done({ ok: false, nick: n, error: e?.message || String(e) }),
        enqueuedAt: Date.now(),
        cancelled: false,
      };
      this.queue.push(job);
      this._pump();

      setTimeout(() => {
        const idx = this.queue.indexOf(job);
        if (idx >= 0) {
          this.queue.splice(idx, 1);
          done({ ok: false, nick: n, error: 'timeout' });
          return;
        }
        if (this.current === job) {
          job.cancelled = true;
          done({ ok: false, nick: n, error: 'timeout' });
        }
      }, JOB_TIMEOUT_MS);
    });
  }

  async _pump() {
    if (this.running) return;
    const job = this.queue.shift();
    if (!job) return;

    this.running = true;
    this.current = job;
    try {
      const result = await this._processJob(job);
      if (!job.cancelled) job.resolve(result);
    } catch (err) {
      if (!job.cancelled) {
        job.resolve({
          ok: false,
          nick: job.nick,
          error: err?.message || String(err),
        });
      }
    } finally {
      this.current = null;
      this.running = false;
      setImmediate(() => this._pump());
    }
  }

  async _processJob(job) {
    const tried = new Set();

    while (true) {
      if (job.cancelled) {
        return { ok: false, nick: job.nick, error: 'timeout' };
      }

      const account = this.pool.pickReady(tried);
      if (!account) {
        return { ok: false, nick: job.nick, error: 'no_accounts' };
      }
      tried.add(account.id);

      const client = this.pool.getClient(account.id);
      if (!client) {
        continue;
      }

      try {
        await this._ensureChannel(client);
        await this._ensureStarted(client, account);

        const bindReply = await this._sendAndWait(
          client,
          `/bind ${job.nick} ${job.password}`,
          (text) => BIND_OK.test(text) || BIND_FULL.test(text),
        );

        if (BIND_FULL.test(bindReply)) {
          console.log(`[binder] account ${account.phone} full`);
          this.pool.markFull(account.id);
          continue; // retry with next TG account
        }

        if (!BIND_OK.test(bindReply)) {
          return {
            ok: false,
            nick: job.nick,
            tgPhone: account.phone,
            error: 'bind_unexpected_reply',
            reply: bindReply.slice(0, 200),
          };
        }

        const twofaReply = await this._sendAndWait(
          client,
          `/2fa ${job.nick}`,
          (text) => TWOFA_OK.test(text),
        );

        return {
          ok: true,
          nick: job.nick,
          tgPhone: account.phone,
          reply: twofaReply.slice(0, 200),
        };
      } catch (err) {
        const msg = err?.message || String(err);
        if (msg === 'reply_timeout') {
          return {
            ok: false,
            nick: job.nick,
            tgPhone: account.phone,
            error: 'reply_timeout',
          };
        }
        console.error(`[binder] error on ${account.phone}:`, msg);
        return {
          ok: false,
          nick: job.nick,
          tgPhone: account.phone,
          error: msg,
        };
      }
    }
  }

  async _ensureChannel(client) {
    try {
      const entity = await client.getInputEntity(FUNTIME_CHANNEL);
      await client.invoke(new Api.channels.JoinChannel({ channel: entity }));
    } catch (err) {
      const msg = err?.errorMessage || err?.message || '';
      // Already participant / private / etc. — continue if already in
      if (/USER_ALREADY_PARTICIPANT|already/i.test(msg)) return;
      // Channel might be a username join via contacts.ResolveUsername + JoinChannel
      // If getInputEntity worked but join failed for other reasons, log and continue
      console.warn(`[binder] join @${FUNTIME_CHANNEL}:`, msg);
    }
  }

  async _ensureStarted(client, account) {
    if (account.started) return;
    await client.sendMessage(FUNAUTH_BOT, { message: '/start' });
    this.pool.markStarted(account.id);
    // Brief pause so bot can process /start
    await sleep(800);
  }

  /**
   * Send a message to FunAuthBot and wait for a matching reply.
   * @param {import('telegram').TelegramClient} client
   * @param {string} message
   * @param {(text: string) => boolean} match
   */
  _sendAndWait(client, message, match) {
    return new Promise(async (resolve, reject) => {
      let settled = false;
      /** @type {((event: any) => Promise<void>) | null} */
      let handler = null;
      const eventFilter = new NewMessage({
        incoming: true,
        fromUsers: [FUNAUTH_BOT],
      });

      const cleanup = () => {
        if (handler) {
          try {
            client.removeEventHandler(handler, eventFilter);
          } catch {
            try { client.removeEventHandler(handler); } catch { /* ignore */ }
          }
          handler = null;
        }
      };

      const timer = setTimeout(() => {
        if (settled) return;
        settled = true;
        cleanup();
        reject(new Error('reply_timeout'));
      }, BIND_REPLY_TIMEOUT_MS);

      handler = async (event) => {
        try {
          const msg = event.message;
          if (!msg) return;
          const text = msg.message || '';
          if (!text || !match(text)) return;
          if (settled) return;
          settled = true;
          clearTimeout(timer);
          cleanup();
          resolve(text);
        } catch {
          /* ignore handler errors */
        }
      };

      client.addEventHandler(handler, eventFilter);

      try {
        await client.sendMessage(FUNAUTH_BOT, { message });
      } catch (err) {
        if (!settled) {
          settled = true;
          clearTimeout(timer);
          cleanup();
          reject(err);
        }
      }
    });
  }
}

function sleep(ms) {
  return new Promise((r) => setTimeout(r, ms));
}
