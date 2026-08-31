package main

import (
	"context"
	"log"
	"strings"
	"time"

	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// Купленные TG: после bind/2fa стираем диалог с FunAuthBot и блочим бота,
// чтобы чужой вход в акк не видел переписку и новые сообщения бота.
// Сессию на диске не трогаем — иначе следующий /2fa некуда слать.
// Перед командой бота — unblock.

func funauthPrepareBotChat(ctx context.Context, acc *funauthAccount) {
	if acc == nil || acc.api == nil {
		return
	}
	peer, err := resolveFunauthBotPeer(ctx, acc.api)
	if err != nil {
		log.Printf("[funauth] unblock resolve %s: %v", acc.meta.Phone, err)
		return
	}
	if peer != nil {
		acc.botID = peer.UserID
	}
	if _, err := acc.api.ContactsUnblock(ctx, &tg.ContactsUnblockRequest{ID: peer}); err != nil {
		if !funauthIgnoreBlockErr(err) {
			log.Printf("[funauth] unblock @%s %s: %v", funauthBotUser, acc.meta.Phone, err)
		}
	}
}

func funauthScrubBotChat(ctx context.Context, acc *funauthAccount) error {
	if acc == nil || acc.api == nil {
		return nil
	}
	peer, err := resolveFunauthBotPeer(ctx, acc.api)
	if err != nil {
		return err
	}
	if peer != nil {
		acc.botID = peer.UserID
	}

	req := &tg.MessagesDeleteHistoryRequest{
		Peer:  peer,
		MaxID: 2147483647,
	}
	req.SetRevoke(true)
	if _, err := acc.api.MessagesDeleteHistory(ctx, req); err != nil {
		if !funauthIgnoreBlockErr(err) {
			return err
		}
	}

	if _, err := acc.api.ContactsBlock(ctx, &tg.ContactsBlockRequest{ID: peer}); err != nil {
		if !funauthIgnoreBlockErr(err) {
			return err
		}
	}
	log.Printf("[funauth] scrub+block @%s on %s", funauthBotUser, acc.meta.Phone)
	return nil
}

func funauthScrubAfterJob(acc *funauthAccount) {
	if acc == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if err := funauthScrubBotChat(ctx, acc); err != nil {
		log.Printf("[funauth] scrub after job %s: %v", acc.meta.Phone, err)
	}
}

func funauthIgnoreBlockErr(err error) bool {
	if err == nil {
		return true
	}
	s := strings.ToLower(err.Error())
	if strings.Contains(s, "already") ||
		strings.Contains(s, "not_blocked") ||
		strings.Contains(s, "user_not_mutual") ||
		strings.Contains(s, "peer_id_invalid") {
		return true
	}
	return tgerr.Is(err, "USER_NOT_MUTUAL_CONTACT", "PEER_ID_INVALID", "CONTACT_ID_INVALID")
}

func (p *funauthPool) accountShouldScrub(acc *funauthAccount) bool {
	if p == nil || acc == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if acc.meta.Full {
		return true
	}
	return p.accountBoundCountLocked(acc.meta.ID) > 0
}

func (p *funauthPool) listConnectedAccounts() []*funauthAccount {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*funauthAccount, 0, len(p.accounts))
	for _, a := range p.accounts {
		if a.ready && a.api != nil {
			out = append(out, a)
		}
	}
	return out
}

func (p *funauthPool) scrubBoundChats(ctx context.Context) int {
	n := 0
	for _, acc := range p.listConnectedAccounts() {
		if ctx.Err() != nil {
			break
		}
		if !p.accountShouldScrub(acc) {
			continue
		}
		if err := funauthScrubBotChat(ctx, acc); err != nil {
			log.Printf("[funauth] scrub bound %s: %v", acc.meta.Phone, err)
			continue
		}
		n++
		time.Sleep(350 * time.Millisecond)
	}
	return n
}

func (p *funauthPool) scheduleScrubBound() {
	goSafe("funauth:scrub-bound", func() {
		time.Sleep(25 * time.Second)
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		defer cancel()
		n := p.scrubBoundChats(ctx)
		log.Printf("[funauth] startup scrub bound chats: %d", n)
	})
}
