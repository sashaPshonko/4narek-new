# Market Data Inventory

Дата: 2026-09-14  
БД: `/root/4narek-new/ml_data/pricing.db`  
Цель: что есть для realistic / counterfactual market modeling. **Не ограничено** тем, что уже ест V9/V10.

---

## Сводка таблиц

| Таблица | n rows | Distinct items | Time range (UTC) | Частота / характер |
|--------|-------:|---------------:|------------------|--------------------|
| `capital_cycles` | ~100k | 35 | 2026-07-15 → 09-14 | ~10 мин / SKU (цикл Go) |
| `trade_events` | ~799k | 34 | 2026-07-15 → 09-14 | каждая сделка buy/sell |
| `ah_book_lots` | ~4.4M | 18 | **2026-09-03 → 09-14** | snapshot лотов AH (плотно) |
| `ah_book_seller_bans` | ~14k | 18 | 09-04 → 09-14 | бан-лист селлеров |
| `ah_sellers` | ~28k | — | — | nick↔id |
| `stock_snapshot_sets` | ~100k | — | 07-15 → 09-14 | 1:1 с циклами |
| `stock_snapshot_rows` | ~402k | 34 | via set_id | on_ah/inv/held/price per item |
| `server_price_events` | 864 | 23 | 07-15 → 09-13 | manual/server min/max |
| `ml_decisions` | ~9k | — | 07-15 → 09-14 | ML payload (gz/json) |
| `ml_shadow` | 0 | — | — | пусто |
| `market_recovery_shadow` | 20 | 5 | 09-09…10 | shadow only |
| `capped_discovery_shadow` | 2 | 1 | 09-09 | shadow only |
| `trusted_min_discovery_shadow` | ~4.5k | 18 | 09-09 → 09-14 | shadow + p10/min/sellers |
| `trusted_min_discovery_jumps` | 0 | — | — | пусто |
| `items` | 34 | — | — | id → category |

---

## 1. `capital_cycles` — наш decision log

**Хранится:** ts, policy, item_id, action, winner, dump/fill/skim/threshold, sales, buys, try_sells, on_ah, inv, held, share, free_slots, need, normal_sales, stock_load, underbuy, price_before/after, nacenka_*, price_floor, step, cooldown, players_online, profit_now, cheap_*, bots_category, cycle_minutes, streaks, **fwd_profit/sells/buys/held 1..3**, fwd_reward, notes.

**Полнота:** главный источник для Jul–Sep; 35 SKU.  
**Связка по времени:** `ts` ≈ конец цикла; join к trades/book по item+окну.  
**Для counterfactual:** до/после смены цены (`price_before≠price_after`), последующие sales/buys/held, fwd_* (осторожно: могут быть заполнены асинхронно; `fwd_done` часто 0).

---

## 2. `trade_events` — факты сделок

**Поля:** ts, item_id, category_type, event_type (buy/sell), price, nacenka, enchants, durability, ref_price.

**Полнота:** Jul–Sep, ~800k.  
**Связка:** точный поток продаж/закупок внутри цикла.  
**CF:** rate sales в окнах после ΔP; распределение цен сделок vs наша list price.

**V9/V10 сейчас:** только sell median + avg nacenka.

---

## 3. `ah_book_lots` — витрина AH

**Поля:** uuid PK, ts, go_type, item_id, price, durability, seller, enchants_json, anarchy, seen_by, seller_id.

**Полнота:** **только с ~3 Sep** (~11 дней), 18 items, 4.4M rows — очень плотно.  
**Связка:** raw p10/p25/median, n lots, unique sellers, UUID concentration per item×time.  
**CF:** внешний market state в момент решения; реакция книги после наших ΔP (если ретенция snapshots достаточна).

**Критично:** покрывает **хвост** периода capital_cycles, не весь Jul–Aug. Для полного Jul–Sep AH replay невозможен без бэкапа.

---

## 4. `ah_book_seller_bans` / `ah_sellers`

Баны и справочник ников. Для ban-filtered min/p10 и «честной» толщины книги (как в Go).

---

## 5. `stock_snapshot_*`

Параллельный снимок флота: on_ah, inv, held, price, nacenka per item на цикл.  
Проверка согласованности с capital_cycles.held / on_ah / inv.

---

## 6. `server_price_events`

Виды: server_max (806), server_min (48), manual_set (10).  
Внешние клипы цены — важны как confounders в event-study.

---

## 7. Shadow / ML таблицы

| Таблица | Полезность для market CF |
|--------|---------------------------|
| `trusted_min_discovery_shadow` | p10, min_ask, sellers, uuid, our/p10 — **хороший** короткий AH feature log с 09-09 |
| `market_recovery_shadow` | мало строк |
| `capped_discovery_shadow` | почти пусто |
| `ml_decisions` | payload состояния на decision; тяжёлый разбор |
| `ml_shadow` | пусто |

---

## 8. Какие поля связать для counterfactual panel

Рекомендуемый ключ: `(item_id, ts_cycle)`.

На каждый capital_cycle:

1. Из цикла: price_before/after, held, sales, buys, action, share, step  
2. Из trades в (ts−cycle_minutes, ts]: sell/buy counts & prices  
3. Из ah_book_lots в [ts−10m, ts] (если ts≥2026-09-03): p10, p50, n_lots, n_sellers, HHI sellers  
4. Из bans: filtered min  
5. Из server_price_events: был ли clamp  
6. Лаги: prev action, Δprice, empty_streak (вычислимо)

Горизонты ответа: +1…+N циклов (~10m) и агрегаты 30m…24h по trades.

---

## 9. Пригодность для CF model

| Подход | Данные | Ограничение |
|--------|--------|-------------|
| Event-study ΔP → sales | capital_cycles Jul–Sep | Confounding политикой |
| Elasticity by ratio×held | cycles + proxy/AH mkt | Associative |
| AH-conditioned demand | book с 09-03 | Короткое окно |
| NN historical states | cycles (+AH late) | Curse of dim; bias |
| Full market replay | нужен полный AH Jul–Sep | **Нет** в текущей БД |

---

## 10. Gaps

1. AH book **не** покрывает Jul–Aug (пик corridor live).  
2. `fwd_*` в cycles ненадёжны как готовый label (`fwd_done=0` массово).  
3. DOI как отдельная колонка в cycles нет — только вычислимо held/sales.  
4. Игроки online есть, но слабо использованы.  
5. Нет явного «buy book» / очереди баеров — только наши buys.

---

## Вывод для research

Максимально реалистичный путь **на имеющемся**:

1. Jul–Sep: event-study + elasticity на `capital_cycles` + `trade_events`.  
2. 03–14 Sep: то же **с настоящим AH p10/lots/sellers**.  
3. Не обещать full counterfactual replay за весь сезон без архива AH.
