package main

import "encoding/json"

// Кто из ботов категории реально может купить товар. Повышать цену имеет смысл, только если
// хоть один бот способен воспользоваться более высоким лимитом закупки: есть деньги и есть слот.
// Иначе запас пуст не из-за цены, а из-за пустых кошельков / забитых слотов — рост цены ничего не даст.

const (
	// Баланс старше этого считаем неизвестным: бот мог получить деньги или потратить их.
	buyerBalanceStaleSec = 30 * 60

	// Стоп-закуп на самом боте (azalea ah.rs: слоты АХ + HELD_SPARE): лоты на АХ + предметы
	// в инвентаре ≥ слоты + 3 → новые лоты не берёт. Слотов 5, а с арендой 10 (поле slots у бота).
	// Считается по сумме, не по отдельным частям. Держать в синхроне с ботом.
	botAHLotSlots  = baseAHSlotsPerBot
	botHeldSpare   = 3
	botBuyStopHeld = botAHLotSlots + botHeldSpare
)

type buyerStats struct {
	Eligible int // живые боты категории на анархии
	Able     int // могут купить (деньги неизвестны или хватает, слоты есть)
	Broke    int // баланс известен и меньше нужной суммы
	Full     int // у бота сработал стоп-закуп (лоты на АХ + инвентарь ≥ 5 + 3)
	// FreeSlots — сколько штук боты, которые могут купить, ещё возьмут до стоп-закупа (8 − занято любыми предметами).
	// Бот без данных о слотах считается свободным целиком.
	FreeSlots int
}

// botCounts — товар → штук из items/inventory бота. Оркестратор присылает карту {"id": n} или список
// ["id", {"id": "...", "count": n}]; разбираем оба вида. Не разобралось — nil (данных нет, бот не блокируется).
func botCounts(raw json.RawMessage) map[string]int {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]float64
	if json.Unmarshal(raw, &m) == nil && m != nil {
		out := make(map[string]int, len(m))
		for k, v := range m {
			out[k] = int(v)
		}
		return out
	}
	var arr []any
	if json.Unmarshal(raw, &arr) == nil {
		out := map[string]int{}
		for _, el := range arr {
			switch v := el.(type) {
			case string:
				if v != "" {
					out[v]++
				}
			case map[string]any:
				id, _ := v["id"].(string)
				if id == "" {
					id, _ = v["go_type"].(string)
				}
				if id == "" {
					continue
				}
				n := 1
				if c, ok := v["count"].(float64); ok && c >= 1 {
					n = int(c)
				}
				out[id] += n
			}
		}
		return out
	}
	return nil
}

// countBuyers — чистая функция. needBalance — минимум денег на одну покупку по новому лимиту.
// Неизвестный баланс или запасы не блокируют: без данных считаем, что бот может купить.
func countBuyers(bots []orchBotNick, goType string, needBalance int) buyerStats {
	var st buyerStats
	for _, b := range bots {
		if b.GoType != goType || !b.Live || !b.OnAnarchy || b.Banned || b.Unstable {
			continue
		}
		st.Eligible++

		full := false
		stopHeld := botBuyStopHeld
		if b.Slots > 0 {
			stopHeld = b.Slots + botHeldSpare
		}
		free := stopHeld
		items, invs := botCounts(b.Items), botCounts(b.Inventory)
		if items != nil || invs != nil {
			// Лимит на боте общий на все товары, не по категории: слоты занимает что угодно.
			held := 0
			for _, n := range items {
				held += n
			}
			for _, n := range invs {
				held += n
			}
			full = held >= stopHeld
			free = stopHeld - held
			if free < 0 {
				free = 0
			}
		}

		broke := false
		if b.Balance != nil && (b.BalanceAgeS == nil || *b.BalanceAgeS <= buyerBalanceStaleSec) {
			broke = *b.Balance < float64(needBalance)
		}

		switch {
		case broke:
			st.Broke++
		case full:
			st.Full++
		default:
			st.Able++
			st.FreeSlots += free
		}
	}
	return st
}

// categoryBuyersLocked — mutex взят (читает clientOrchBots и itemsConfig).
func categoryBuyersLocked(goType string, needBalance int) buyerStats {
	var total buyerStats
	for _, bots := range clientOrchBots {
		st := countBuyers(bots, goType, needBalance)
		total.Eligible += st.Eligible
		total.Able += st.Able
		total.Broke += st.Broke
		total.Full += st.Full
		total.FreeSlots += st.FreeSlots
	}
	return total
}
