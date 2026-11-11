package main

import (
	"sync"
	"time"
)

// Заявки ботов на лот АХ. Все боты одной категории работают через один Go, поэтому именно он решает,
// кто покупает лот: ПЕРВЫЙ заявивший владеет им lotClaimTTL, остальным отказ (claim_result granted=false).
// Повторная заявка того же владельца продлевает срок. Если бот передумал (лот изменился, невыгоден),
// он шлёт unclaim, и лот сразу свободен для других.
const lotClaimTTL = 15 * time.Second

// Окно выбора: первые lotClaimWindow после первой заявки на лот владельца можно сменить, если пришла заявка
// от бота, у которого накоплено меньше (held), то есть лот достаётся тому, кому он нужнее. Боты кликают
// не раньше 2 секунд после заявки, так что окно (1 с) успевает закрыться до покупки.
const lotClaimWindow = time.Second

// Бот с полным АХ (5 лотов) уступает ботам, у которых АХ ещё не заполнен: цель — забить АХ у всех, а не
// копить товар в инвентаре. Его заявку в окне lotClaimWindow может перебить любой бот с неполным АХ
// (независимо от накопления). Клика полного бота мы НЕ задерживаем: лишняя задержка стоила бы прозеванных
// лотов, а уступать некому, если другой бот не заявил лот сам.
const fullAHRankPenalty = 1000

// clickTolerance: перебить владельца можно, только если новый бот кликнет не позже владельца с запасом
// столько. Иначе уступка замедлила бы покупку и мы рисковали бы потерять лот. Сберечь лот важнее.
const clickTolerance = 200 * time.Millisecond

// clickUnknown — бот не сообщил, когда кликнет: такая заявка никого не перебивает.
const clickUnknown = time.Duration(-1)

// heldUnknown — бот не сообщил, сколько у него накоплено (старый оркестратор): считаем худшим и не даём
// ему перебивать чужую заявку.
const heldUnknown = 1 << 20

type lotClaim struct {
	Owner   string
	Held    int // ранг: накопление; у бота с полным АХ + fullAHRankPenalty
	Full    bool
	ClickAt time.Time // когда владелец планирует кликнуть (нулевое — неизвестно)
	At      time.Time
	FirstAt time.Time
}

var (
	lotClaimsMu sync.Mutex
	lotClaims   = map[string]lotClaim{}
)

func pruneLotClaimsLocked(now time.Time) {
	for u, c := range lotClaims {
		if now.Sub(c.At) > lotClaimTTL {
			delete(lotClaims, u)
		}
	}
}

// tryClaimLot — заявка без сведений о накоплении: ведёт себя как «первый занял — владеет».
func tryClaimLot(uuid, owner string, now time.Time) (bool, string) {
	return tryClaimLotHeld(uuid, owner, heldUnknown, now)
}

// tryClaimLotHeld — заявка бота с неполным АХ (см. tryClaimLotRanked).
func tryClaimLotHeld(uuid, owner string, held int, now time.Time) (bool, string) {
	return tryClaimLotRanked(uuid, owner, held, false, clickUnknown, now)
}

// tryClaimLotRanked возвращает granted и текущего владельца лота. Первый заявивший владеет лотом; в окне
// (lotClaimWindow) его может сменить бот с лучшим рангом:
// неполный АХ лучше полного, при одинаковом классе меньшее накопление лучше. При равенстве остаётся первый.
func tryClaimLotRanked(uuid, owner string, held int, fullAH bool, clickIn time.Duration, now time.Time) (bool, string) {
	var clickAt time.Time
	if clickIn >= 0 {
		clickAt = now.Add(clickIn)
	}
	rank := held
	if fullAH && held < heldUnknown {
		rank += fullAHRankPenalty
	}
	lotClaimsMu.Lock()
	defer lotClaimsMu.Unlock()
	pruneLotClaimsLocked(now)
	first := now
	if c, ok := lotClaims[uuid]; ok {
		first = c.FirstAt
		if c.Owner != owner {
			if now.Sub(c.FirstAt) > lotClaimWindow || rank >= c.Held {
				return false, c.Owner
			}
			// уступка допустима только если новый бот кликнет не позже владельца (с запасом clickTolerance):
			// иначе лот ушёл бы к тому, кто купит его позже, и мы рисковали бы им
			if clickAt.IsZero() || c.ClickAt.IsZero() || clickAt.After(c.ClickAt.Add(clickTolerance)) {
				return false, c.Owner
			}
			// перебил более подходящий бот
		}
	}
	prevClick := clickAt
	if c, ok := lotClaims[uuid]; ok && c.Owner == owner && clickAt.IsZero() {
		prevClick = c.ClickAt
	}
	lotClaims[uuid] = lotClaim{Owner: owner, Held: rank, Full: fullAH, ClickAt: prevClick, At: now, FirstAt: first}
	return true, owner
}

// releaseLot снимает заявку, только если лот числится за этим владельцем.
func releaseLot(uuid, owner string, now time.Time) bool {
	lotClaimsMu.Lock()
	defer lotClaimsMu.Unlock()
	pruneLotClaimsLocked(now)
	if c, ok := lotClaims[uuid]; ok && c.Owner == owner {
		delete(lotClaims, uuid)
		return true
	}
	return false
}

// lotClaimsSnapshot — действующие заявки [{uuid, username}] для рассылки оркестраторам.
func lotClaimsSnapshot(now time.Time) []map[string]string {
	lotClaimsMu.Lock()
	defer lotClaimsMu.Unlock()
	pruneLotClaimsLocked(now)
	out := make([]map[string]string, 0, len(lotClaims))
	for u, c := range lotClaims {
		out = append(out, map[string]string{"uuid": u, "username": c.Owner})
	}
	return out
}
