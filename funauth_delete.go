package main

import (
	"context"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/tg"
)

// Ферма: TG-акк удаляем, только когда он заполнен — к нему привязано столько ботов фермы, сколько разрешено в панели
// («Ботов на один TG», по умолчанию 1; при лимите 10 — ровно 10). Пока места ещё есть, TG остаётся в пуле для следующих
// привязок и для /2fa. Удаление — в Telegram (account.deleteAccount) и из пула, с диска тоже. Необратимо.
// Если к аккаунту нет доступа (клиент офлайн, сессия отозвана/деактивирована) — убираем его из пула.
// Выключить удаление: FUNAUTH_DELETE_TG_AFTER_BIND=0. Овнера (не ферменный бот) не трогаем — его TG может понадобиться снова.

func funauthDeleteAfterBindEnabled() bool {
	v := strings.TrimSpace(os.Getenv("FUNAUTH_DELETE_TG_AFTER_BIND"))
	return v != "0" && !strings.EqualFold(v, "false") && !strings.EqualFold(v, "off")
}

// funauthTGFull — TG заполнен: ботов фермы на нём не меньше лимита из панели.
func funauthTGFull(farmBound, limit int) bool {
	return farmBound >= clampPerAccount(limit)
}

// funauthNoAccessErr — Telegram говорит, что сессии больше нет (удалить аккаунт через неё нельзя).
func funauthNoAccessErr(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToUpper(err.Error())
	for _, m := range []string{"AUTH_KEY_UNREGISTERED", "AUTH_KEY_INVALID", "SESSION_REVOKED", "SESSION_EXPIRED", "USER_DEACTIVATED", "AUTH_KEY_DUPLICATED"} {
		if strings.Contains(s, m) {
			return true
		}
	}
	return false
}

func (p *funauthPool) deleteTelegramAfterBind(acc *funauthAccount, nick string) {
	if acc == nil || p == nil || !funauthDeleteAfterBindEnabled() || !nickIsFarmBot(nick) {
		return
	}
	id, phone, api := acc.meta.ID, acc.meta.Phone, acc.api
	p.mu.Lock()
	farmBound, limit := p.accountFarmCountLocked(id), p.perAccountLocked()
	p.mu.Unlock()
	if !funauthTGFull(farmBound, limit) {
		log.Printf("[funauth] TG %s после %s: ботов %d из %d — оставляю в пуле", phone, nick, farmBound, limit)
		return
	}
	if api == nil {
		_ = p.remove(id)
		log.Printf("[funauth] TG %s заполнен (%d/%d) после %s, но доступа нет (клиент офлайн) — убран из пула", phone, farmBound, limit, nick)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if _, err := api.AccountDeleteAccount(ctx, &tg.AccountDeleteAccountRequest{Reason: ""}); err != nil {
		if funauthNoAccessErr(err) {
			_ = p.remove(id)
			log.Printf("[funauth] TG %s заполнен (%d/%d) после %s, доступа нет (%v) — убран из пула", phone, farmBound, limit, nick, err)
			return
		}
		log.Printf("[funauth] delete TG %s после %s: %v (акк остаётся в пуле)", phone, nick, err)
		return
	}
	_ = p.remove(id)
	log.Printf("[funauth] TG %s заполнен (%d/%d), удалён в Telegram и из пула (после bind %s)", phone, farmBound, limit, nick)
	enqueueTelegramMessage("🗑 FunAuth: TG "+phone+" удалён (ботов "+strconv.Itoa(farmBound)+") после привязки `"+nick+"`", "Markdown")
}
