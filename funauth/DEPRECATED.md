# FunAuth Node sidecar — DEPRECATED

Нативный Go FunAuth (gotd) встроен в сервер `4narek-new`.

- UI / API: `http://HOST:8080/funauth/`
- Env: `TELEGRAM_API_ID`, `TELEGRAM_API_HASH` (можно в `funauth/.env` — Go подхватит)
- Сессии: `funauth_sessions/` рядом с процессом

Node `funauth/` (GramJS на :8091) больше не нужен для runtime.
Можно удалить позже (`npm` / `node_modules`) — не ломая Go-сборку.
