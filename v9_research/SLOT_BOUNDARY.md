# book2: global margin slots across SKUs

**Status:** LIVE — `book2AllocateByMargin` / `book2EnsureGlobalAlloc`.

## Проблема

5 слотов × много SKU. Per-SKU «K-й дешёвый» забивает АХ sword7 с щелью 200k,
пока в книге лежит mega со щелью 500k.

## Решение

1. По всем SKU категории собрать лоты с `sell−price ≥ softMin`
2. Отсортировать по **абсолютной марже** ↓
3. Взять top `bots×5` (ёмкость АХ категории)
4. `buyMax[sku]` = макс. цена среди взятых лотов этого sku  
   нет в alloc → **не покупаем** (buyMax=0)

Слот всегда достаётся самой жирной щели, даже между разными предметами.

Кэш alloc 2 мин на go_type. Лог: `globalMarg cap=… [megasword×12 sword7×3 …]`.
