package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Надёжность FunAuth: проверка TG-аккаунтов, ники с неподтверждённой 2FA и пауза между попытками /2fa.

const (
	// funauthTwoFAPendingFile — ники, которые привязаны к TG, но выключение 2FA не подтвердилось.
	// Пока такой ник есть на TG, этот TG не удаляем: 2FA выключается только с него.
	funauthTwoFAPendingFile = "twofa_pending.json"

	funauthHealthEvery   = 3 * time.Minute
	funauthHealthTimeout = 20 * time.Second

	// funauthErrTGLost — ник привязан к TG, которого больше нет в пуле (удалён, сессия отозвана).
	// Выключить 2FA для такого ника нельзя ничем, нужен новый ник.
	funauthErrTGLost = "tg_lost"
)

// ---------- ники с неподтверждённой 2FA ----------

func (p *funauthPool) twofaPendingPath() string {
	return filepath.Join(p.dir, funauthTwoFAPendingFile)
}

func (p *funauthPool) loadTwoFAPending() {
	raw, err := os.ReadFile(p.twofaPendingPath())
	if err != nil {
		return
	}
	var m map[string]bool
	if err := json.Unmarshal(raw, &m); err != nil {
		log.Printf("[funauth] bad %s: %v", funauthTwoFAPendingFile, err)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, v := range m {
		if key := funauthNickKey(k); key != "" && v {
			p.twofaPending[key] = true
		}
	}
	log.Printf("[funauth] 2FA не подтверждена у %d ник(ов)", len(p.twofaPending))
}

func (p *funauthPool) saveTwoFAPendingLocked() {
	raw, err := json.MarshalIndent(p.twofaPending, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(p.twofaPendingPath(), raw, 0o600); err != nil {
		log.Printf("[funauth] save %s: %v", funauthTwoFAPendingFile, err)
	}
}

// markTwoFAPending — ник остался за TG, а 2FA выключить не удалось (или исход /bind неясен).
func (p *funauthPool) markTwoFAPending(nick string) {
	key := funauthNickKey(nick)
	if key == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.twofaPending[key] {
		return
	}
	p.twofaPending[key] = true
	p.saveTwoFAPendingLocked()
}

// clearTwoFAPending — 2FA у ника выключена (bind или /2fa ответили «выключено»).
func (p *funauthPool) clearTwoFAPending(nick string) {
	key := funauthNickKey(nick)
	if key == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.twofaPending[key] {
		return
	}
	delete(p.twofaPending, key)
	p.saveTwoFAPendingLocked()
}

// accountPendingNicksLocked — ники на TG, у которых 2FA не подтверждена.
func (p *funauthPool) accountPendingNicksLocked(accountID string) []string {
	var out []string
	for nick, id := range p.nicks {
		if id == accountID && p.twofaPending[nick] {
			out = append(out, nick)
		}
	}
	return out
}

// ---------- где искать TG для /2fa ----------

// twoFATarget — TG, к которому привязан ник. lost = ник за TG числится, но самого TG в пуле нет.
func (p *funauthPool) twoFATarget(nick string) (acc *funauthAccount, mapped bool, lost bool) {
	key := funauthNickKey(nick)
	p.mu.Lock()
	defer p.mu.Unlock()
	id, ok := p.nicks[key]
	if !ok || id == "" {
		return nil, false, false
	}
	a := p.accounts[id]
	if a == nil {
		return nil, true, true
	}
	return a, true, false
}

// ---------- пауза между попытками /2fa ----------

type funauthTwoFAState struct {
	until    time.Time
	fails    int
	lastErr  string
	loggedAt time.Time
}

var (
	funauthTwoFAMu     sync.Mutex
	funauthTwoFAByNick = map[string]*funauthTwoFAState{}
)

// funauthTwoFADelay — сколько не трогать ник после неудачи. Ник без TG — раз в 6 ч (нужна замена ника),
// остальное — 1, 2, 4 … 30 мин.
func funauthTwoFADelay(errStr string, fails int) time.Duration {
	switch {
	case errStr == funauthErrTGLost:
		return 6 * time.Hour
	case errStr == "no_bound_account":
		return 30 * time.Minute
	}
	d := time.Minute
	for i := 1; i < fails && d < 30*time.Minute; i++ {
		d *= 2
	}
	if d > 30*time.Minute {
		d = 30 * time.Minute
	}
	return d
}

// funauthTwoFAWait — ник ещё на паузе после прошлой неудачи? logNow — пора напомнить в логе (раз в 10 мин).
func funauthTwoFAWait(nick string) (wait time.Duration, lastErr string, logNow bool) {
	key := funauthNickKey(nick)
	funauthTwoFAMu.Lock()
	defer funauthTwoFAMu.Unlock()
	st := funauthTwoFAByNick[key]
	if st == nil {
		return 0, "", false
	}
	wait = time.Until(st.until)
	if wait <= 0 {
		return 0, "", false
	}
	if time.Since(st.loggedAt) >= 10*time.Minute {
		st.loggedAt = time.Now()
		logNow = true
	}
	return wait, st.lastErr, logNow
}

func funauthTwoFANote(nick string, ok bool, errStr string) {
	key := funauthNickKey(nick)
	funauthTwoFAMu.Lock()
	defer funauthTwoFAMu.Unlock()
	if ok {
		delete(funauthTwoFAByNick, key)
		return
	}
	if errStr == "busy" || strings.HasPrefix(errStr, "skipped:") {
		return
	}
	st := funauthTwoFAByNick[key]
	if st == nil {
		st = &funauthTwoFAState{}
		funauthTwoFAByNick[key] = st
	}
	st.fails++
	st.lastErr = errStr
	st.until = time.Now().Add(funauthTwoFADelay(errStr, st.fails))
	st.loggedAt = time.Now()
}

// ---------- проверка TG-аккаунтов ----------

// dropAccount — к TG больше нет доступа (сессия отозвана, аккаунт удалён/заблокирован): убираем из пула.
// Ники за ним остаются в nicks.json: по ним видно, что 2FA у них теперь выключить нечем.
func (p *funauthPool) dropAccount(id, reason string) {
	p.mu.Lock()
	acc := p.accounts[id]
	phone := id
	if acc != nil {
		phone = acc.meta.Phone
	}
	lost := 0
	for _, aid := range p.nicks {
		if aid == id {
			lost++
		}
	}
	p.mu.Unlock()
	if acc == nil {
		return
	}
	_ = p.remove(id)
	log.Printf("[funauth] TG %s убран из пула: нет доступа (%s); ников за ним: %d", phone, truncateRunes(reason, 120), lost)
	enqueueTelegramMessage("⚠️ FunAuth: TG "+phone+" без доступа ("+truncateRunes(reason, 60)+") — убран из пула", "")
}

func (p *funauthPool) startHealthLoop() {
	goImmortal("funauth:health", func() {
		for {
			select {
			case <-p.ctx.Done():
				return
			case <-time.After(funauthHealthEvery):
			}
			p.healthCheckOnce()
		}
	})
}

// healthCheckOnce — живые TG пингуем (updates.getState): «сессии нет» → убираем из пула.
// Остановившиеся клиенты (обрыв, markDead) поднимаем заново.
func (p *funauthPool) healthCheckOnce() {
	type item struct {
		meta    funauthAccountMeta
		acc     *funauthAccount
		alive   bool
		stopped bool
	}
	p.mu.Lock()
	items := make([]item, 0, len(p.accounts))
	for _, a := range p.accounts {
		items = append(items, item{meta: a.meta, acc: a, alive: a.ready && a.api != nil, stopped: a.stopped})
	}
	p.mu.Unlock()

	for _, it := range items {
		if it.alive {
			// бинд/2FA на этом TG идёт — не мешаем
			if !it.acc.jobMu.TryLock() {
				continue
			}
			api := it.acc.api
			if api == nil {
				it.acc.jobMu.Unlock()
				continue
			}
			ctx, cancel := context.WithTimeout(p.ctx, funauthHealthTimeout)
			_, err := api.UpdatesGetState(ctx)
			cancel()
			it.acc.jobMu.Unlock()
			if err == nil {
				continue
			}
			if funauthNoAccessErr(err) {
				p.dropAccount(it.meta.ID, err.Error())
				continue
			}
			log.Printf("[funauth] health %s: %v", it.meta.Phone, err)
			continue
		}
		if it.stopped {
			log.Printf("[funauth] health %s: клиент остановлен — переподключаю", it.meta.Phone)
			meta := it.meta
			goSafe("funauth:health-reconnect:"+meta.ID, func() {
				p.connectAccount(meta, pickFarmLoginSOCKS())
			})
		}
	}
}
