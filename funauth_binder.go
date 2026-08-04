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
	"github.com/gotd/td/tgerr"
)

const (
	funauthBindReplyTimeout = 45 * time.Second
	funauthJobTimeout       = 120 * time.Second
)

var (
	funauthBindOK  = regexp.MustCompile(`(?i)был привязан`)
	// «[Бот] У Вас уже много привязанных аккаунтов»
	funauthBindFull = regexp.MustCompile(`(?i)у вас уже много привязанных|уже много привязанных|много привязанных аккаунт`)
	funauthTwofaOK = regexp.MustCompile(`(?i)выключено|Подтверждение входа`)
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
func (b *funauthBinder) Bind(nick, password string) funauthBindResult {
	n := strings.TrimSpace(nick)
	p := password
	if n == "" || strings.TrimSpace(p) == "" {
		return funauthBindResult{OK: false, Nick: n, Error: "nick_password_required"}
	}
	if b.pool == nil || !b.pool.configured() {
		return funauthBindResult{OK: false, Nick: n, Error: "funauth_not_configured"}
	}

	job := &funauthBindJob{
		nick:       n,
		password:   p,
		enqueuedAt: time.Now(),
		result:     make(chan funauthBindResult, 1),
	}

	b.mu.Lock()
	b.queue = append(b.queue, job)
	b.mu.Unlock()
	go b.pump()

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

	b.mu.Lock()
	b.current = nil
	b.running = false
	more := len(b.queue) > 0
	b.mu.Unlock()
	if more {
		go b.pump()
	}
}

func (b *funauthBinder) processJob(job *funauthBindJob) funauthBindResult {
	tried := make(map[string]struct{})

	for {
		job.mu.Lock()
		cancelled := job.cancelled
		job.mu.Unlock()
		if cancelled {
			return funauthBindResult{OK: false, Nick: job.nick, Error: "timeout"}
		}

		acc := b.pool.pickReady(tried)
		if acc == nil {
			return funauthBindResult{OK: false, Nick: job.nick, Error: "no_accounts"}
		}
		tried[acc.meta.ID] = struct{}{}

		phone := acc.meta.Phone
		ctx, cancel := context.WithTimeout(context.Background(), funauthJobTimeout)
		result, retry := b.runOnAccount(ctx, acc, job)
		cancel()
		if retry {
			log.Printf("[funauth] account %s full, trying next", phone)
			continue
		}
		return result
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
		b.pool.markFull(acc.meta.ID)
		return funauthBindResult{}, true
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
