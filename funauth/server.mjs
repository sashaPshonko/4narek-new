import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import express from 'express';
import { AccountPool } from './pool.mjs';
import { Binder } from './binder.mjs';

const __dirname = path.dirname(fileURLToPath(import.meta.url));

function loadDotEnv() {
  const envPath = path.join(__dirname, '.env');
  if (!fs.existsSync(envPath)) return;
  const text = fs.readFileSync(envPath, 'utf8');
  for (const line of text.split('\n')) {
    const trimmed = line.trim();
    if (!trimmed || trimmed.startsWith('#')) continue;
    const eq = trimmed.indexOf('=');
    if (eq < 0) continue;
    const key = trimmed.slice(0, eq).trim();
    let val = trimmed.slice(eq + 1).trim();
    if (
      (val.startsWith('"') && val.endsWith('"')) ||
      (val.startsWith("'") && val.endsWith("'"))
    ) {
      val = val.slice(1, -1);
    }
    if (!(key in process.env)) process.env[key] = val;
  }
}

loadDotEnv();

const PORT = Number(process.env.PORT) || 8091;

if (!process.env.TELEGRAM_API_ID || !process.env.TELEGRAM_API_HASH) {
  console.error('Set TELEGRAM_API_ID and TELEGRAM_API_HASH (see .env.example)');
  process.exit(1);
}

const pool = new AccountPool();
const binder = new Binder(pool);

const app = express();
app.use(express.json({ limit: '64kb' }));
app.use(express.static(path.join(__dirname, 'static')));

function asyncRoute(fn) {
  return (req, res) => {
    Promise.resolve(fn(req, res)).catch((err) => {
      console.error('[api]', err);
      const status = /required|not_started/i.test(err?.message || '') ? 400 : 500;
      res.status(status).json({
        ok: false,
        error: err?.errorMessage || err?.message || String(err),
      });
    });
  };
}

app.get('/api/health', (_req, res) => {
  res.json({
    ok: true,
    accounts: pool.list().length,
    ready: pool.list().filter((a) => a.ready && !a.full).length,
    queue: binder.status().length,
  });
});

app.get('/api/accounts', (_req, res) => {
  res.json(pool.list());
});

app.post(
  '/api/login/start',
  asyncRoute(async (req, res) => {
    const { phone } = req.body || {};
    const result = await pool.loginStart(phone);
    res.json({ ok: true, ...result });
  }),
);

app.post(
  '/api/login/code',
  asyncRoute(async (req, res) => {
    const { phone, code } = req.body || {};
    const result = await pool.loginCode(phone, code);
    res.json({ ok: true, ...result });
  }),
);

app.post(
  '/api/login/password',
  asyncRoute(async (req, res) => {
    const { phone, password } = req.body || {};
    const result = await pool.loginPassword(phone, password);
    res.json({ ok: true, ...result });
  }),
);

app.delete(
  '/api/accounts/:id',
  asyncRoute(async (req, res) => {
    await pool.remove(req.params.id);
    res.json({ ok: true });
  }),
);

app.post(
  '/api/bind',
  asyncRoute(async (req, res) => {
    const { nick, password } = req.body || {};
    const result = await binder.bind(nick, password);
    res.json(result);
  }),
);

app.get('/api/queue', (_req, res) => {
  res.json(binder.status());
});

await pool.init();

app.listen(PORT, () => {
  console.log(`[funauth] http://127.0.0.1:${PORT}`);
});

async function shutdown() {
  console.log('[funauth] shutting down…');
  await pool.shutdown();
  process.exit(0);
}

process.on('SIGINT', shutdown);
process.on('SIGTERM', shutdown);
