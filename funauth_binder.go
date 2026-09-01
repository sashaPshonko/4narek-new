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
	funauthBindReplyTimeout = 45 * time.Second
	funauthJobTimeout       = 120 * time.Second
	funauthHistoryLimit     = 120
)

var (
	funauthBindOK = regexp.MustCompile(`(?i)был привязан`)
	// «[Бот] У Вас уже много привязанных аккаунтов»
	funauthBindFull        = regexp.MustCompile(`(?i)у вас уже много привязанных|уже много привязанных|много привязанных аккаунт`)
	funauthTwofaOK         = regexp.MustCompile(`(?i)выключено|Подтверждение входа`)
	funauthHistoryBindHint = regexp.MustCompile(`(?i)(/bind\s+|был привязан|/2fa\s+|подтверждение входа|привязан)`)
)

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
	result     chan funauthBindResult
	cancelled  bool
	mu         sync.Mutex
}

type funauthBinder struct {
	pool *funauthPool

	mu      sync.Mutex
	queue   []*funauthBindJob
	current *funauthBindJob
	running bool
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
			"enqueuedAt": j.enqueuedAt.UnixMilli(),
		})
	}
	var current any
	if b.current != nil {
		current = map[string]any{
			"nick":       b.current.nick,
			"enqueuedAt": b.current.enqueuedAt.UnixMilli(),
		}
	}
	length := len(b.queue)
	if b.running {
		length++
	}
	return map[string]any{
		"running": b.running,
		"current": current,
		"pending": pending,
		"length":  length,
	}
}

func (b *funauthBinder) queueLen() int {
	st := b.status()
	if n, ok := st["length"].(int); ok {
		return n
	}
	return 0
}

// Bind enqueues a job and waits up to ~120s for the result.
func (b *funauthBinder) Bind(nick, password string, anarchy int) funauthBindResult {
	return b.enqueue(nick, password, anarchy, "bind")
}

// TwoFA sends only `/2fa nick` from the TG account that already bound this nick.
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
		if !removed && b.current == job {
			job.mu.Lock()
			job.cancelled = true
			job.mu.Unlock()
		}
		b.mu.Unlock()
		return funauthBindResult{OK: false, Nick: n, Error: "timeout"}
	}
}

func (b *funauthBinder) pump() {
	b.mu.Lock()
	if b.running {
		b.mu.Unlock()
		return
	}
	if len(b.queue) == 0 {
		b.mu.Unlock()
		return
	}
	job := b.queue[0]
	b.queue = b.queue[1:]
	b.running = true
	b.current = job
	b.mu.Unlock()

	defer func() {
		if recovered := recover(); recovered != nil {
			logPanic("funauth:pump", recovered)
		}
		b.mu.Lock()
		b.current = nil
		b.running = false
		more := len(b.queue) > 0
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
			return funauthBindResult{OK: false, Nick: job.nick, Error: "timeout"}
		}

		acc, diag := b.pool.pickForAnarchyBindDiag(job.nick, job.anarchy, tried)
		if acc == nil {
			errCode := "no_accounts"
			if diag.Busy > 0 && diag.Offline == 0 && diag.Full == 0 {
				errCode = "all_accounts_busy"
			}
			log.Printf(
				"[funauth] pick fail %s an%d: err=%s offline=%d full=%d busy=%d excluded=%d",
				job.nick, job.anarchy, errCode, diag.Offline, diag.Full, diag.Busy, diag.Excluded,
			)
			return funauthBindResult{OK: false, Nick: job.nick, Error: errCode}
		}
		tried[acc.meta.ID] = struct{}{}

		acc = b.pool.ensureNickSOCKS(acc, job.nick)
		phone := acc.meta.Phone
		ctx, cancel := context.WithTimeout(context.Background(), funauthJobTimeout)
		result, retry := b.runOnAccount(ctx, acc, job)
		cancel()
		if retry {
			log.Printf("[funauth] account %s saturated, trying next", phone)
			continue
		}
		if result.OK {
			b.pool.afterBindSuccess(job.nick, acc.meta.ID, job.anarchy)
		}
		return result
	}
}

func (b *funauthBinder) processTwoFA(job *funauthBindJob) funauthBindResult {
	tried := make(map[string]struct{})
	tryAcc := func(acc *funauthAccount, why string) *funauthBindResult {
		if acc == nil {
			return nil
		}
		if _, seen := tried[acc.meta.ID]; seen {
			return nil
		}
		tried[acc.meta.ID] = struct{}{}
		acc = b.pool.ensureNickSOCKS(acc, job.nick)
		log.Printf("[funauth] 2fa %s via %s (%s)", job.nick, acc.meta.Phone, why)
		ctx, cancel := context.WithTimeout(context.Background(), funauthJobTimeout)
		res := b.runTwoFAOnAccount(ctx, acc, job.nick)
		cancel()
		if res.OK {
			b.pool.rememberNick(job.nick, acc.meta.ID)
			return &res
		}
		log.Printf("[funauth] 2fa skip %s on %s: %s", job.nick, acc.meta.Phone, res.Error)
		return nil
	}

	// 1) карта nick→tg
	if preferred := b.pool.accountForNick(job.nick); preferred != nil {
		if res := tryAcc(preferred, "nicks.json"); res != nil {
			return *res
		}
	}

	// 2) история чата с FunAuthBot (старые бинды без карты)
	histCtx, histCancel := context.WithTimeout(context.Background(), 60*time.Second)
	fromHistory := b.findAccountsByBotHistory(histCtx, job.nick)
	histCancel()
	for _, acc := range fromHistory {
		if res := tryAcc(acc, "history"); res != nil {
			return *res
		}
	}

	// 3) остальные готовые
	log.Printf("[funauth] 2fa %s: map/history пусто — перебор остальных", job.nick)
	for {
		job.mu.Lock()
		cancelled := job.cancelled
		job.mu.Unlock()
		if cancelled {
			return funauthBindResult{OK: false, Nick: job.nick, Error: "timeout"}
		}
		acc := b.pool.pickReadyAny(tried)
		if acc == nil {
			return funauthBindResult{OK: false, Nick: job.nick, Error: "no_bound_account"}
		}
		if res := tryAcc(acc, "fallback"); res != nil {
			return *res
		}
	}
}

// findAccountsByBotHistory — у кого в диалоге с FunAuthBot есть этот ник + bind/2fa след.
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
		// ник в диалоге + намёк на bind/2fa (или просто /bind|/2fa с ником)
		if funauthHistoryBindHint.MatchString(text) ||
			strings.Contains(text, "/bind") ||
			strings.Contains(text, "/2fa") {
			return true, nil
		}
		// исходящий `/bind nick …` / ответ бота с ником — тоже ок
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
		// nothing
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

	// любое ответное сообщение бота — чтобы быстро отсеять чужой акк
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

func (b *funauthBinder) runOnAccount(ctx context.Context, acc *funauthAccount, job *funauthBindJob) (funauthBindResult, bool) {
	api := acc.api
	if api == nil {
		return funauthBindResult{OK: false, Nick: job.nick, Error: "account_offline"}, false
	}
	if acc.botID == 0 {
		if id, err := resolveFunauthBotID(ctx, api); err == nil {
			acc.botID = id
		}
	}
	sender := message.NewSender(api)
	funauthPrepareBotChat(ctx, acc)
	defer funauthScrubAfterJob(acc)

	if err := funauthEnsureChannel(ctx, sender); err != nil {
		log.Printf("[funauth] join @%s: %v", funauthChannel, err)
	}

	if !acc.meta.Started {
		if _, err := sender.Resolve(funauthBotUser).Text(ctx, "/start"); err != nil {
			return funauthBindResult{
				OK: false, Nick: job.nick, TgPhone: acc.meta.Phone,
				Error: err.Error(),
			}, false
		}
		b.pool.markStarted(acc.meta.ID)
		time.Sleep(800 * time.Millisecond)
	}

	bindReply, err := funauthSendAndWait(ctx, sender, acc, "/bind "+job.nick+" "+job.password, func(text string) bool {
		return funauthBindOK.MatchString(text) || funauthBindFull.MatchString(text)
	})
	if err != nil {
		errStr := err.Error()
		if errStr == "reply_timeout" {
			return funauthBindResult{
				OK: false, Nick: job.nick, TgPhone: acc.meta.Phone, Error: "reply_timeout",
			}, false
		}
		return funauthBindResult{
			OK: false, Nick: job.nick, TgPhone: acc.meta.Phone, Error: errStr,
		}, false
	}

	if funauthBindFull.MatchString(bindReply) {
		b.pool.syncAccountRosterFull(acc.meta.ID)
		return funauthBindResult{
			OK: false, Nick: job.nick, TgPhone: acc.meta.Phone,
			Error: "tg_bind_limit", Reply: truncateRunes(bindReply, 200),
		}, false
	}
	if !funauthBindOK.MatchString(bindReply) {
		return funauthBindResult{
			OK: false, Nick: job.nick, TgPhone: acc.meta.Phone,
			Error: "bind_unexpected_reply", Reply: truncateRunes(bindReply, 200),
		}, false
	}

	twofaReply, err := funauthSendAndWait(ctx, sender, acc, "/2fa "+job.nick, func(text string) bool {
		return funauthTwofaOK.MatchString(text)
	})
	if err != nil {
		errStr := err.Error()
		if errStr == "reply_timeout" {
			return funauthBindResult{
				OK: false, Nick: job.nick, TgPhone: acc.meta.Phone, Error: "reply_timeout",
			}, false
		}
		return funauthBindResult{
			OK: false, Nick: job.nick, TgPhone: acc.meta.Phone, Error: errStr,
		}, false
	}

	return funauthBindResult{
		OK:      true,
		Nick:    job.nick,
		TgPhone: acc.meta.Phone,
		Reply:   truncateRunes(twofaReply, 200),
	}, false
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
	// Drain stale bot messages.
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
