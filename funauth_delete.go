package main

import (
	"context"
	"log"
	"os"
	"strings"
	"time"

	"github.com/gotd/td/tg"
)

// Ферма: 1 MC = 1 TG. После успешного /bind + /2fa (2FA выключена) TG-акк больше не нужен —
// удаляем его в Telegram (account.deleteAccount) и убираем из пула и с диска.
// Необратимо. Выключить: FUNAUTH_DELETE_TG_AFTER_BIND=0.
// Овнера (не ферменный бот) не трогаем — его TG может понадобиться снова.

func funauthDeleteAfterBindEnabled() bool {
	v := strings.TrimSpace(os.Getenv("FUNAUTH_DELETE_TG_AFTER_BIND"))
	return v != "0" && !strings.EqualFold(v, "false") && !strings.EqualFold(v, "off")
}

func (p *funauthPool) deleteTelegramAfterBind(acc *funauthAccount, nick string) {
	if acc == nil || p == nil || !funauthDeleteAfterBindEnabled() || !nickIsFarmBot(nick) {
		return
	}
	id, phone, api := acc.meta.ID, acc.meta.Phone, acc.api
	if api == nil {
		log.Printf("[funauth] delete TG %s после %s: клиент офлайн, пропуск", phone, nick)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if _, err := api.AccountDeleteAccount(ctx, &tg.AccountDeleteAccountRequest{Reason: ""}); err != nil {
		log.Printf("[funauth] delete TG %s после %s: %v (акк остаётся в пуле)", phone, nick, err)
		return
	}
	_ = p.remove(id)
	log.Printf("[funauth] TG %s удалён в Telegram и из пула (после bind %s)", phone, nick)
	enqueueTelegramMessage("🗑 FunAuth: TG "+phone+" удалён после привязки `"+nick+"`", "Markdown")
}
