# book2: global margin + fill safety

**Status:** LIVE — prefer fat margin, **never ban while slots free**.

## Баг

Резали buyMax=0 по top-(bots×5) из книги. Жирные лоты в книге ≠ выкуп.
Слоты пустели: запретили sword7, mega не купился.

## Правила

1. Считаем `free = capacity − sumAH`
2. Global ranking только на **free** слотов (не на всю ёмкость)
3. **`free > 0`:** buyMax = max(global, per-SKU fill K-й дешёвый) — SKU никогда не баним
4. **`free = 0`:** селективность — победители global, иначе best-1 этого SKU (всё ещё не полный ban)

Слот 500k предпочтительнее 200k, но пустой слот хуже любого из них.
