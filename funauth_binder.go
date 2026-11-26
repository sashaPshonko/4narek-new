package main

import (
	"context"
	"errors"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

const (
	// ↓ было 45s — теперь 15s: мёртвый TG отсеивается быстрее
	funauthBindReplyTimeout = 15 * time.Second
	// ↑ было 120s — теперь 180s: успеваем перебрать больше TG
	funauthJobTimeout   = 180 * time.Second
	funauthHistoryLimit = 120

	// Сколько биндов/2FA могут идти одновременно. TG-логины идут отдельно
	// (через funauthPool), их этот лимит не касается.
	funauthMaxConcurrentJobs = 3

	// Если все акки busy (TG-логин/чужой бинд) — ждём столько и пробуем снова.
	funauthBusyRetryEvery = 5 * time.Second
)

var (
	funauthBindOK          = regexp.MustCompile(`(?i)был привязан`)
	funauthBindFull        = regexp.MustCompile(`(?i)у вас уже много привязанных|уже много привязанных|много привязанных аккаунт`)
	funauthTwofaOK         = regexp.MustCompile(`(?i)выключено|Подтверждение входа`)
	funauthHistoryBindHint = regexp.MustCompile(`(?i)(/bind\s+|был привязан|/2fa\s+|подтверждение входа|привязан)`)
)

// funauthRetryNextAccount — ошибка сессии/слоя TG: этот акк пропускаем, пробуем следующий.
func funauthRetryNextAccount(errStr string) bool {
	s := strings.ToLower(strings.TrimSpace(errStr))
	if s == "" {
		return false
	}
	needles := []string{
		"connection_layer_invalid",
		"auth_key_unregistered",
		"auth_key_invalid",
		"auth_key_duplicated",
		"session_revoked",
		"session_expired",
		"user_deactivated",
		"user_deactivated_ban",
		"not authorized",
		"account_offline",
		"reply_timeout",
	}
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

type funauthBindResult struct {
	OK      bool   `json:"ok"`
	Nick    string `json:"nick"`
	TgPhone string `json:"tgPhone,omitempty"`
	Error   string `json:"error,omitempty"`
	Reply   string `json:"reply,omitempty"`
}

type funauthBindJob struct {
	nick       string
	password   string
	anarchy    int
	mode       string // "bind" | "twofa"
	enqueuedAt time.Time
	startedAt  time.Time
	result     chan funauthBindResult
	cancelled  bool
	mu         sync.Mutex
}

type funauthBinder struct {
	pool *funauthPool

	mu       sync.Mutex
	queue    []*funauthBindJob
	currents []*funauthBindJob // было current *funauthBindJob — теперь N
	running  int               // было bool — теперь счётчик
}

func newFunauthBinder(pool *funauthPool) *funauthBinder {
	return &funauthBinder{pool: pool}
}

func (b *funauthBinder) status() map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	pending := make([]map[string]any, 0, len(b.queue))
	for _, j := range b.queue {
		pending = append(pending, map[string]any{
			"nick":       j.nick,
			"mode":       j.mode,
			"enqueuedAt": j.enqueuedAt.UnixMilli(),
		})
	}
	currents := make([]map[string]any, 0, len(b.currents))
	for _, j := range b.currents {
		startedAt := int64(0)
		if !j.startedAt.IsZero() {
			startedAt = j.startedAt.UnixMilli()
		}
		currents = append(currents, map[string]any{
			"nick":       j.nick,
			"mode":       j.mode,
			"enqueuedAt": j.enqueuedAt.UnixMilli(),
			"startedAt":  startedAt,
		})
	}
	return map[string]any{
		"running":  b.running,
		"maxJobs":  funauthMaxConcurrentJobs,
		"currents": currents,
		"pending":  pending,
		"length":   len(b.queue) + b.running,
	}
}

func (b *funauthBinder) queueLen() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.queue) + b.running
}

// activeBindsCount — сколько сейчас выполняется bind/2FA (для /health).
func (b *funauthBinder) activeBindsCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, j := range b.currents {
		if j != nil {
			n++
		}
	}
	return n
}

// activeTGCount — заглушка: TG-логины не идут через binder.
// Если в funauthPool есть свой счётчик — можно вернуть его.
func (b *funauthBinder) activeTGCount() int {
	if b.pool == nil {
		return 0
	}
	return b.pool.activeTGLoginCount()
}

func (b *funauthBinder) Bind(nick, password string, anarchy int) funauthBindResult {
	return b.enqueue(nick, password, anarchy, "bind")
}

func (b *funauthBinder) TwoFA(nick string, anarchy int) funauthBindResult {
	return b.enqueue(nick, "", anarchy, "twofa")
}

func (b *funauthBinder) enqueue(nick, password string, anarchy int, mode string) funauthBindResult {
	n := strings.TrimSpace(nick)
	if n == "" {
		return funauthBindResult{OK: false, Nick: n, Error: "nick_required"}
	}
	if mode == "bind" && strings.TrimSpace(password) == "" {
		return funauthBindResult{OK: false, Nick: n, Error: "nick_password_required"}
	}
	if b.pool == nil || !b.pool.configured() {
		return funauthBindResult{OK: false, Nick: n, Error: "funauth_not_configured"}
	}

	job := &funauthBindJob{
		nick:       n,
		password:   password,
		anarchy:    anarchy,
		mode:       mode,
		enqueuedAt: time.Now(),
		result:     make(chan funauthBindResult, 1),
	}

	b.mu.Lock()
	b.queue = append(b.queue, job)
	b.mu.Unlock()
	goSafe("funauth:pump", b.pump)

	timer := time.NewTimer(funauthJobTimeout)
	defer timer.Stop()

	select {
	case res := <-job.result:
		return res
	case <-timer.C:
		b.mu.Lock()
		removed := false
		for i, j := range b.queue {
			if j == job {
				b.queue = append(b.queue[:i], b.queue[i+1:]...)
				removed = true
				break
			}
		}
		if !removed {
			for _, j := range b.currents {
				if j == job {
					job.mu.Lock()
					job.cancelled = true
					job.mu.Unlock()
					break
				}
			}
		}
		b.mu.Unlock()
		// ВАЖНО: не "timeout" — это может быть «стоял в очереди, пока другие
		// бинды шли». UI должен показать "busy" и попробовать позже.
		return funauthBindResult{OK: false, Nick: n, Error: "busy"}
	}
}

func (b *funauthBinder) pump() {
	b.mu.Lock()
	if b.running >= funauthMaxConcurrentJobs {
		b.mu.Unlock()
		return
	}
	if len(b.queue) == 0 {
		b.mu.Unlock()
		return
	}
	job := b.queue[0]
	b.queue = b.queue[1:]
	b.running++
	job.startedAt = time.Now()
	b.currents = append(b.currents, job)
	b.mu.Unlock()

	defer func() {
		if recovered := recover(); recovered != nil {
			logPanic("funauth:pump", recovered)
		}
		b.mu.Lock()
		for i, j := range b.currents {
			if j == job {
				b.currents = append(b.currents[:i], b.currents[i+1:]...)
				break
			}
		}
		b.running--
		more := len(b.queue) > 0 && b.running < funauthMaxConcurrentJobs
		b.mu.Unlock()
		if more {
			goSafe("funauth:pump", b.pump)
		}
	}()

	res := b.processJob(job)
	job.mu.Lock()
	cancelled := job.cancelled
	job.mu.Unlock()
	if !cancelled {
		select {
		case job.result <- res:
		default:
		}
	}
}

func (b *funauthBinder) processJob(job *funauthBindJob) funauthBindResult {
	if job.mode == "twofa" {
		return b.processTwoFA(job)
	}
	tried := make(map[string]struct{})

	for {
		job.mu.Lock()
		cancelled := job.cancelled
		job.mu.Unlock()
		if cancelled {
			return funauthBindResult{OK: false, Nick: job.nick, Error: "busy"}
		}

		acc, diag := b.pool.pickForAnarchyBindDiag(job.nick, job.anarchy, tried)
		if acc == nil {
			// ВАЖНО: если кто-то busy (TG-логин / чужой бинд) — ждём,
			// а не возвращаем no_accounts. Иначе TG-логин блокирует все бинды.
			if diag.Busy > 0 {
				log.Printf(
					"[funauth] %s: %d busy, wait %s",
					job.nick, diag.Busy, funauthBusyRetryEvery,
				)
				select {
				case <-time.After(funauthBusyRetryEvery):
					continue
				}
			}
			errCode := "no_accounts"
			if diag.Offline == 0 && diag.Full == 0 && diag.Busy == 0 {
				errCode = "no_accounts"
			} else if diag.Full > 0 && diag.Offline == 0 && diag.Busy == 0 {
				errCode = "all_accounts_busy"
			}
			log.Printf(
				"[funauth] pick fail %s an%d: err=%s offline=%d full=%d busy=%d excluded=%d",
				job.nick, job.anarchy, errCode, diag.Offline, diag.Full, diag.Busy, diag.Excluded,
			)
			return funauthBindResult{OK: false, Nick: job.nick, Error: errCode}
		}
		tried[acc.meta.ID] = struct{}{}

		claimed := acc
		acc = b.pool.ensureNickSOCKS(acc, job.nick)
		phone := acc.meta.Phone
		ctx, cancel := context.WithTimeout(context.Background(), funauthJobTimeout)
		result, retry := b.runOnAccount(ctx, acc, job)
		cancel()
		b.pool.releaseClaim(claimed)
		if claimed != acc {
			b.pool.releaseClaim(acc)
		}
		if funauthUnconfirmed(result.Error) {
			// FunAuthBot не ответил, но привязка могла пройти. Не бросаем этот TG: запоминаем ник за ним,
			// чтобы повторный bind / 2FA пошли сюда же. Два «не дождались» подряд на одном TG — он правда мёртв.
			if n := b.pool.noteUnconfirmed(acc); n >= 2 {
				b.pool.resetUnconfirmed(acc)
				b.pool.markDead(acc.meta.ID, result.Error)
				log.Printf("[funauth] account %s: %s второй раз подряд → dead, пробую следующий", phone, result.Error)
				continue
			}
			b.pool.rememberNick(job.nick, acc.meta.ID)
			b.pool.markTwoFAPending(job.nick)
			log.Printf("[funauth] %s on %s: %s — TG оставлен за ником, без scrub/dead", job.nick, phone, result.Error)
			return result
		}
		if result.OK {
			b.pool.resetUnconfirmed(acc)
		}
		if wait := funauthFloodWait(result.Error); wait > 0 && !result.OK {
			// Telegram просит подождать: этот аккаунт пропускаем на это время и пробуем другой
			b.pool.markFlood(acc.meta.ID, wait)
			log.Printf("[funauth] account %s FLOOD_WAIT %s — пропускаю, пробую следующий", phone, wait.Round(time.Minute))
			continue
		}
		if retry {
			why := "full"
			if funauthNoAccessErr(errors.New(result.Error)) {
				why = "no access"
				b.pool.dropAccount(acc.meta.ID, result.Error)
			} else if funauthRetryNextAccount(result.Error) {
				why = "dead"
				b.pool.markDead(acc.meta.ID, result.Error)
			}
			log.Printf("[funauth] account %s %s (%s), trying next", phone, why, result.Error)
			continue
		}
		if result.OK {
			b.pool.afterBindSuccess(job.nick, acc.meta.ID, job.anarchy)
			b.pool.clearTwoFAPending(job.nick)
			b.pool.deleteTelegramAfterBind(acc, job.nick)
		}
		return result
	}
}

// processTwoFA — `/2fa ник` только с того TG, к которому ник привязан: FunAuthBot выключает 2FA лишь оттуда.
// Ника нет в nicks.json — ищем TG по переписке с ботом. С чужих TG /2fa не шлём: бесполезно и злит FunAuthBot.
// TG ника удалён или потерял сессию — tg_lost: выключить 2FA больше нечем, нику нужна замена.
func (b *funauthBinder) processTwoFA(job *funauthBindJob) funauthBindResult {
	acc, mapped, lost := b.pool.twoFATarget(job.nick)
	if lost {
		log.Printf("[funauth] 2fa %s: TG ника больше нет в пуле — выключить 2FA нечем, нужен новый ник", job.nick)
		return funauthBindResult{OK: false, Nick: job.nick, Error: funauthErrTGLost}
	}
	if mapped {
		return b.twoFAOn(acc, job.nick, "nicks.json")
	}

	histCtx, histCancel := context.WithTimeout(context.Background(), 60*time.Second)
	fromHistory := b.findAccountsByBotHistory(histCtx, job.nick)
	histCancel()
	last := funauthBindResult{OK: false, Nick: job.nick, Error: "no_bound_account"}
	for _, a := range fromHistory {
		res := b.twoFAOn(a, job.nick, "history")
		if res.OK {
			return res
		}
		last = res
	}
	if len(fromHistory) == 0 {
		log.Printf("[funauth] 2fa %s: ник не привязан ни к одному TG пула", job.nick)
	}
	return last
}

// twoFAOn — один `/2fa` на конкретном TG. Сессии нет — TG из пула убираем.
func (b *funauthBinder) twoFAOn(acc *funauthAccount, nick, why string) funauthBindResult {
	b.pool.mu.Lock()
	ready := acc.ready && acc.api != nil
	b.pool.mu.Unlock()
	if !ready {
		// клиент переподключается (проверка пула поднимет) — попробуем при следующем запросе
		return funauthBindResult{OK: false, Nick: nick, TgPhone: acc.meta.Phone, Error: "account_offline"}
	}
	acc = b.pool.ensureNickSOCKS(acc, nick)
	log.Printf("[funauth] 2fa %s via %s (%s)", nick, acc.meta.Phone, why)
	ctx, cancel := context.WithTimeout(context.Background(), funauthJobTimeout)
	res := b.runTwoFAOnAccount(ctx, acc, nick)
	cancel()
	if res.OK {
		b.pool.rememberNick(nick, acc.meta.ID)
		b.pool.clearTwoFAPending(nick)
		// TG мог ждать удаления только из-за этого ника
		b.pool.deleteTelegramAfterBind(acc, nick)
		return res
	}
	if funauthNoAccessErr(errors.New(res.Error)) {
		b.pool.dropAccount(acc.meta.ID, res.Error)
	} else if wait := funauthFloodWait(res.Error); wait > 0 {
		b.pool.markFlood(acc.meta.ID, wait)
	}
	log.Printf("[funauth] 2fa skip %s on %s: %s %s", nick, acc.meta.Phone, res.Error, truncateRunes(res.Reply, 120))
	return res
}

func (b *funauthBinder) findAccountsByBotHistory(ctx context.Context, nick string) []*funauthAccount {
	needle := strings.ToLower(strings.TrimSpace(nick))
	if needle == "" {
		return nil
	}
	var hits []*funauthAccount
	for _, acc := range b.pool.listConnectedAccounts() {
		ok, err := funauthHistoryHasNick(ctx, acc, needle)
		if err != nil {
			log.Printf("[funauth] history %s: %v", acc.meta.Phone, err)
			continue
		}
		if ok {
			log.Printf("[funauth] history hit: nick=%s tg=%s", nick, acc.meta.Phone)
			hits = append(hits, acc)
		}
	}
	return hits
}

func funauthHistoryHasNick(ctx context.Context, acc *funauthAccount, nickLower string) (bool, error) {
	api := acc.api
	if api == nil {
		return false, errors.New("account_offline")
	}
	peer, err := resolveFunauthBotPeer(ctx, api)
	if err != nil {
		return false, err
	}
	if peer != nil && acc.botID == 0 {
		acc.botID = peer.UserID
	}

	hist, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
		Peer:  peer,
		Limit: funauthHistoryLimit,
	})
	if err != nil {
		return false, err
	}

	msgs := funauthExtractMessages(hist)
	for _, m := range msgs {
		text := strings.ToLower(strings.TrimSpace(m))
		if text == "" {
			continue
		}
		if !strings.Contains(text, nickLower) {
			continue
		}
		if funauthHistoryBindHint.MatchString(text) ||
			strings.Contains(text, "/bind") ||
			strings.Contains(text, "/2fa") {
			return true, nil
		}
		return true, nil
	}
	return false, nil
}

func resolveFunauthBotPeer(ctx context.Context, api *tg.Client) (*tg.InputPeerUser, error) {
	res, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{
		Username: funauthBotUser,
	})
	if err != nil {
		return nil, err
	}
	for _, u := range res.Users {
		user, ok := u.(*tg.User)
		if ok && strings.EqualFold(user.Username, funauthBotUser) {
			return &tg.InputPeerUser{
				UserID:     user.ID,
				AccessHash: user.AccessHash,
			}, nil
		}
	}
	return nil, errors.New("funauth bot not resolved")
}

func funauthExtractMessages(hist tg.MessagesMessagesClass) []string {
	var out []string
	add := func(list []tg.MessageClass) {
		for _, mc := range list {
			msg, ok := mc.(*tg.Message)
			if !ok || msg == nil {
				continue
			}
			if s := strings.TrimSpace(msg.Message); s != "" {
				out = append(out, s)
			}
		}
	}
	switch h := hist.(type) {
	case *tg.MessagesMessages:
		add(h.Messages)
	case *tg.MessagesMessagesSlice:
		add(h.Messages)
	case *tg.MessagesChannelMessages:
		add(h.Messages)
	case *tg.MessagesMessagesNotModified:
	}
	return out
}

func (b *funauthBinder) runTwoFAOnAccount(ctx context.Context, acc *funauthAccount, nick string) funauthBindResult {
	api := acc.api
	if api == nil {
		return funauthBindResult{OK: false, Nick: nick, TgPhone: acc.meta.Phone, Error: "account_offline"}
	}
	if acc.botID == 0 {
		if id, err := resolveFunauthBotID(ctx, api); err == nil {
			acc.botID = id
		}
	}
	acc.jobMu.Lock()
	defer acc.jobMu.Unlock()
	sender := message.NewSender(api)
	funauthPrepareBotChat(ctx, acc)
	defer funauthScrubAfterJob(acc)
	if err := funauthEnsureChannel(ctx, sender); err != nil {
		log.Printf("[funauth] join @%s: %v", funauthChannel, err)
	}
	if !acc.meta.Started {
		if _, err := sender.Resolve(funauthBotUser).Text(ctx, "/start"); err != nil {
			return funauthBindResult{OK: false, Nick: nick, TgPhone: acc.meta.Phone, Error: err.Error()}
		}
		b.pool.markStarted(acc.meta.ID)
		time.Sleep(800 * time.Millisecond)
	}

	reply, err := funauthSendAndWait(ctx, sender, acc, "/2fa "+nick, func(text string) bool {
		return strings.TrimSpace(text) != ""
	})
	if err != nil {
		return funauthBindResult{OK: false, Nick: nick, TgPhone: acc.meta.Phone, Error: err.Error()}
	}
	if !funauthTwofaOK.MatchString(reply) {
		return funauthBindResult{
			OK: false, Nick: nick, TgPhone: acc.meta.Phone,
			Error: "twofa_not_accepted", Reply: truncateRunes(reply, 200),
		}
	}
	return funauthBindResult{
		OK: true, Nick: nick, TgPhone: acc.meta.Phone, Reply: truncateRunes(reply, 200),
	}
}

func (b *funauthBinder) runOnAccount(ctx context.Context, acc *funauthAccount, job *funauthBindJob) (res funauthBindResult, retry bool) {
	api := acc.api
	if api == nil {
		return funauthBindResult{OK: false, Nick: job.nick, Error: "account_offline"}, false
	}
	acc.jobMu.Lock()
	defer acc.jobMu.Unlock()
	if acc.botID == 0 {
		if id, err := resolveFunauthBotID(ctx, api); err == nil {
			acc.botID = id
		}
	}
	sender := message.NewSender(api)
	funauthPrepareBotChat(ctx, acc)
	// Исход неясен (привязка могла пройти) — переписку с ботом не стираем: по ней 2FA найдёт этот TG.
	defer func() {
		if !funauthUnconfirmed(res.Error) {
			funauthScrubAfterJob(acc)
		}
	}()

	if err := funauthEnsureChannel(ctx, sender); err != nil {
		log.Printf("[funauth] join @%s: %v", funauthChannel, err)
		errStr := err.Error()
		if funauthRetryNextAccount(errStr) {
			return funauthBindResult{
				OK: false, Nick: job.nick, TgPhone: acc.meta.Phone, Error: errStr,
			}, true
		}
	}

	if !acc.meta.Started {
		if _, err := sender.Resolve(funauthBotUser).Text(ctx, "/start"); err != nil {
			errStr := err.Error()
			retry := funauthRetryNextAccount(errStr)
			return funauthBindResult{
				OK: false, Nick: job.nick, TgPhone: acc.meta.Phone,
				Error: errStr,
			}, retry
		}
		b.pool.markStarted(acc.meta.ID)
		time.Sleep(800 * time.Millisecond)
	}

	// Прошлый раз исход был неясен и ник оставлен за этим TG — сначала пробуем просто выключить 2FA.
	if prev := b.pool.accountForNick(job.nick); prev != nil && prev.meta.ID == acc.meta.ID {
		if p := funauthProbeTwoFA(ctx, sender, acc, job.nick); p.ok {
			log.Printf("[funauth] %s: уже привязан к %s, 2FA выкл без /bind", job.nick, acc.meta.Phone)
			return funauthBindResult{OK: true, Nick: job.nick, TgPhone: acc.meta.Phone, Reply: truncateRunes(p.reply, 200)}, false
		}
	}

	bindReply, err := funauthSendAndWait(ctx, sender, acc, "/bind "+job.nick+" "+job.password, func(text string) bool {
		return funauthBindOK.MatchString(text) || funauthBindFull.MatchString(text)
	})
	if err != nil {
		errStr := err.Error()
		if errStr == "reply_timeout" {
			// Ответа на /bind не дождались — но FunTime мог ник уже привязать (так было с cakisuteram:
			// привязан, а /2fa не отправлен → потом вход только через Telegram). Проверяем через /2fa.
			p := funauthProbeTwoFA(ctx, sender, acc, job.nick)
			switch {
			case p.ok:
				log.Printf("[funauth] %s: ответ на /bind не пришёл, но /2fa прошёл — привязан, 2FA выкл", job.nick)
				return funauthBindResult{OK: true, Nick: job.nick, TgPhone: acc.meta.Phone, Reply: truncateRunes(p.reply, 200)}, false
			case p.answered && !p.sawBind:
				// Бот ответил на /2fa отказом — ника за этим TG нет, привязка не прошла. Старое поведение.
				return funauthBindResult{
					OK: false, Nick: job.nick, TgPhone: acc.meta.Phone, Error: "reply_timeout", Reply: truncateRunes(p.reply, 200),
				}, true
			case p.sawBind:
				return funauthBindResult{OK: false, Nick: job.nick, TgPhone: acc.meta.Phone, Error: funauthErrTwofaUnconfirmed, Reply: truncateRunes(p.reply, 200)}, false
			default:
				return funauthBindResult{OK: false, Nick: job.nick, TgPhone: acc.meta.Phone, Error: funauthErrBindUnconfirmed}, false
			}
		}
		return funauthBindResult{
			OK: false, Nick: job.nick, TgPhone: acc.meta.Phone, Error: errStr,
		}, funauthRetryNextAccount(errStr)
	}

	if funauthBindFull.MatchString(bindReply) {
		b.pool.markFull(acc.meta.ID)
		return funauthBindResult{
			OK: false, Nick: job.nick, TgPhone: acc.meta.Phone,
			Error: "tg_bind_limit", Reply: truncateRunes(bindReply, 200),
		}, true
	}
	if !funauthBindOK.MatchString(bindReply) {
		return funauthBindResult{
			OK: false, Nick: job.nick, TgPhone: acc.meta.Phone,
			Error: "bind_unexpected_reply", Reply: truncateRunes(bindReply, 200),
		}, false
	}

	// Ник уже привязан к этому TG. Что бы дальше ни случилось — на другой TG не уходим и этот мёртвым не считаем:
	// 2FA можно выключить только отсюда. Две попытки /2fa.
	var p funauthTwoFAProbe
	for attempt := 0; attempt < 2 && ctx.Err() == nil; attempt++ {
		p = funauthProbeTwoFA(ctx, sender, acc, job.nick)
		if p.ok {
			return funauthBindResult{
				OK:      true,
				Nick:    job.nick,
				TgPhone: acc.meta.Phone,
				Reply:   truncateRunes(p.reply, 200),
			}, false
		}
	}
	errStr := funauthErrTwofaUnconfirmed
	if p.err != "" && p.err != "reply_timeout" {
		errStr += ": " + p.err
	}
	return funauthBindResult{
		OK: false, Nick: job.nick, TgPhone: acc.meta.Phone, Error: errStr, Reply: truncateRunes(p.reply, 200),
	}, false
}

const (
	// funauthErrBindUnconfirmed — на /bind и /2fa ответа нет: привязан ли ник, неизвестно.
	funauthErrBindUnconfirmed = "bind_unconfirmed"
	// funauthErrTwofaUnconfirmed — ник привязан к TG, а 2FA выключить не удалось.
	funauthErrTwofaUnconfirmed = "twofa_unconfirmed"
)

func funauthUnconfirmed(errStr string) bool {
	return strings.HasPrefix(errStr, funauthErrBindUnconfirmed) || strings.HasPrefix(errStr, funauthErrTwofaUnconfirmed)
}

type funauthTwoFAProbe struct {
	ok       bool   // 2FA выключена
	answered bool   // бот ответил на /2fa (не «был привязан»)
	sawBind  bool   // пока ждали — пришло запоздалое «был привязан»
	reply    string // ответ на /2fa
	err      string
}

// funauthProbeTwoFA — шлёт `/2fa ник` и ждёт ответ. Запоздалое «был привязан» по /bind не считается ответом,
// но запоминается (значит ник привязан к этому TG).
func funauthProbeTwoFA(ctx context.Context, sender *message.Sender, acc *funauthAccount, nick string) funauthTwoFAProbe {
	var p funauthTwoFAProbe
	if ctx.Err() != nil {
		p.err = "reply_timeout"
		return p
	}
	reply, err := funauthSendAndWait(ctx, sender, acc, "/2fa "+nick, func(text string) bool {
		if funauthBindOK.MatchString(text) {
			p.sawBind = true
			return false
		}
		return strings.TrimSpace(text) != ""
	})
	if err != nil {
		p.err = err.Error()
		return p
	}
	p.answered = true
	p.reply = reply
	p.ok = funauthTwofaOK.MatchString(reply)
	return p
}

func funauthEnsureChannel(ctx context.Context, sender *message.Sender) error {
	_, err := sender.Resolve(funauthChannel).Join(ctx)
	if err == nil {
		return nil
	}
	if tgerr.Is(err, "USER_ALREADY_PARTICIPANT") || strings.Contains(strings.ToLower(err.Error()), "already") {
		return nil
	}
	return err
}

func funauthSendAndWait(
	ctx context.Context,
	sender *message.Sender,
	acc *funauthAccount,
	text string,
	match func(string) bool,
) (string, error) {
drain:
	for {
		select {
		case <-acc.botMsg:
		default:
			break drain
		}
	}

	waitCtx, cancel := context.WithTimeout(ctx, funauthBindReplyTimeout)
	defer cancel()

	if _, err := sender.Resolve(funauthBotUser).Text(waitCtx, text); err != nil {
		return "", err
	}

	for {
		select {
		case <-waitCtx.Done():
			return "", errors.New("reply_timeout")
		case msg := <-acc.botMsg:
			if match(msg) {
				return msg, nil
			}
		}
	}
}

func truncateRunes(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

// isBusy — только для конкретного ника. Не смотрит на общую очередь.
func (b *funauthBinder) isBusy(nick string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := funauthNickKey(nick)
	for _, j := range b.currents {
		if j != nil && funauthNickKey(j.nick) == key {
			return true
		}
	}
	for _, j := range b.queue {
		if funauthNickKey(j.nick) == key {
			return true
		}
	}
	return false
}
