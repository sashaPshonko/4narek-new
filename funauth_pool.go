package main

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/tg"
)

const (
	funauthSessionsDir = "funauth_sessions"
	funauthBotUser     = "FunAuthBot"
	funauthChannel     = "funtime"
	// Как у Telegram Desktop (как в tg-export) — без .env / my.telegram.org
	funauthDesktopAPIID   = 2040
	funauthDesktopAPIHash = "b18441a1ff607e10a989891a5462e627"
)

var errFunauthNotConfigured = errors.New("funauth_not_configured")

type funauthAccountMeta struct {
	ID       string `json:"id"`
	Phone    string `json:"phone"`
	Username string `json:"username,omitempty"`
	Full     bool   `json:"full"`
	Started  bool   `json:"started"`
}

type funauthAccountView struct {
	ID       string `json:"id"`
	Phone    string `json:"phone"`
	Username string `json:"username"`
	Ready    bool   `json:"ready"`
	Full     bool   `json:"full"`
	Started  bool   `json:"started"`
}

type funauthAccount struct {
	meta   funauthAccountMeta
	ready  bool
	api    *tg.Client
	botID  int64
	botMsg chan string
	cancel context.CancelFunc
}

type funauthLoginResult struct {
	view funauthAccountView
	err  error
}

// channelAuth waits for code/password from HTTP handlers.
type channelAuth struct {
	phone string

	codeSentOnce sync.Once
	codeSent     chan struct{}

	needPassOnce sync.Once
	needPass     chan struct{}

	codeCh chan string
	passCh chan string
}

func newChannelAuth(phone string) *channelAuth {
	return &channelAuth{
		phone:    phone,
		codeSent: make(chan struct{}),
		needPass: make(chan struct{}),
		codeCh:   make(chan string, 1),
		passCh:   make(chan string, 1),
	}
}

func (a *channelAuth) Phone(context.Context) (string, error) { return a.phone, nil }

func (a *channelAuth) Code(ctx context.Context, _ *tg.AuthSentCode) (string, error) {
	a.codeSentOnce.Do(func() { close(a.codeSent) })
	select {
	case code := <-a.codeCh:
		return code, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (a *channelAuth) Password(ctx context.Context) (string, error) {
	a.needPassOnce.Do(func() { close(a.needPass) })
	select {
	case p := <-a.passCh:
		return p, nil
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (a *channelAuth) AcceptTermsOfService(context.Context, tg.HelpTermsOfService) error {
	return errors.New("sign_up_not_supported")
}

func (a *channelAuth) SignUp(context.Context) (auth.UserInfo, error) {
	return auth.UserInfo{}, errors.New("sign_up_not_supported")
}

type funauthPendingLogin struct {
	id     string
	phone  string
	auth   *channelAuth
	ctx    context.Context
	cancel context.CancelFunc
	done   chan funauthLoginResult
}

type funauthPool struct {
	mu sync.Mutex

	apiID   int
	apiHash string
	dir     string

	accounts map[string]*funauthAccount
	pending  map[string]*funauthPendingLogin

	ctx    context.Context
	cancel context.CancelFunc
}

func newFunauthPool() *funauthPool {
	ctx, cancel := context.WithCancel(context.Background())
	return &funauthPool{
		apiID:    funauthDesktopAPIID,
		apiHash:  funauthDesktopAPIHash,
		dir:      funauthSessionsDir,
		accounts: make(map[string]*funauthAccount),
		pending:  make(map[string]*funauthPendingLogin),
		ctx:      ctx,
		cancel:   cancel,
	}
}

func (p *funauthPool) configured() bool {
	return p != nil && p.apiID != 0 && p.apiHash != ""
}

func (p *funauthPool) init() {
	if err := os.MkdirAll(p.dir, 0o755); err != nil {
		log.Printf("[funauth] mkdir %s: %v", p.dir, err)
		return
	}
	log.Printf("[funauth] api_id=%d (Telegram Desktop)", p.apiID)
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		log.Printf("[funauth] read sessions: %v", err)
		return
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(p.dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			log.Printf("[funauth] read %s: %v", path, err)
			continue
		}
		var meta funauthAccountMeta
		if err := json.Unmarshal(raw, &meta); err != nil || meta.ID == "" {
			log.Printf("[funauth] bad meta %s", path)
			continue
		}
		if _, err := os.Stat(p.sessionPath(meta.ID)); err != nil {
			log.Printf("[funauth] session missing for %s", meta.ID)
			continue
		}
		n++
		go p.connectAccount(meta)
	}
	log.Printf("[funauth] loading %d account(s) from %s", n, p.dir)
}

func (p *funauthPool) sessionPath(id string) string {
	return filepath.Join(p.dir, id+".session")
}

func (p *funauthPool) metaPath(id string) string {
	return filepath.Join(p.dir, id+".json")
}

func (p *funauthPool) saveMeta(meta funauthAccountMeta) error {
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p.metaPath(meta.ID), raw, 0o600)
}

func normalizeFunauthPhone(phone string) string {
	var b strings.Builder
	for _, r := range phone {
		if r == '+' || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (p *funauthPool) list() []funauthAccountView {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]funauthAccountView, 0, len(p.accounts))
	for _, a := range p.accounts {
		out = append(out, a.view())
	}
	return out
}

func (a *funauthAccount) view() funauthAccountView {
	return funauthAccountView{
		ID:       a.meta.ID,
		Phone:    a.meta.Phone,
		Username: a.meta.Username,
		Ready:    a.ready,
		Full:     a.meta.Full,
		Started:  a.meta.Started,
	}
}

func (p *funauthPool) readyCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, a := range p.accounts {
		if a.ready && !a.meta.Full && a.api != nil {
			n++
		}
	}
	return n
}

func (p *funauthPool) pickReady(exclude map[string]struct{}) *funauthAccount {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.accounts {
		if _, skip := exclude[a.meta.ID]; skip {
			continue
		}
		if a.ready && !a.meta.Full && a.api != nil {
			return a
		}
	}
	return nil
}

func (p *funauthPool) markFull(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a := p.accounts[id]
	if a == nil {
		return
	}
	a.meta.Full = true
	if err := p.saveMeta(a.meta); err != nil {
		log.Printf("[funauth] save meta %s: %v", id, err)
	}
}

func (p *funauthPool) markStarted(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a := p.accounts[id]
	if a == nil {
		return
	}
	a.meta.Started = true
	if err := p.saveMeta(a.meta); err != nil {
		log.Printf("[funauth] save meta %s: %v", id, err)
	}
}

func (p *funauthPool) connectAccount(meta funauthAccountMeta) {
	dispatcher := tg.NewUpdateDispatcher()
	acc := &funauthAccount{
		meta:   meta,
		botMsg: make(chan string, 32),
	}
	dispatcher.OnNewMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewMessage) error {
		return p.handleBotMessage(acc, e, u.Message)
	})

	client := telegram.NewClient(p.apiID, p.apiHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: p.sessionPath(meta.ID)},
		UpdateHandler:  dispatcher,
	})
	ctx, cancel := context.WithCancel(p.ctx)
	acc.cancel = cancel

	p.mu.Lock()
	if old := p.accounts[meta.ID]; old != nil && old.cancel != nil {
		old.cancel()
	}
	p.accounts[meta.ID] = acc
	p.mu.Unlock()

	err := client.Run(ctx, func(ctx context.Context) error {
		status, err := client.Auth().Status(ctx)
		if err != nil {
			return err
		}
		if !status.Authorized {
			log.Printf("[funauth] session not authorized for %s", meta.Phone)
			return errors.New("not authorized")
		}
		self, err := client.Self(ctx)
		if err != nil {
			return err
		}
		api := client.API()
		botID, err := resolveFunauthBotID(ctx, api)
		if err != nil {
			log.Printf("[funauth] resolve @%s: %v (will retry on bind)", funauthBotUser, err)
		}

		p.mu.Lock()
		acc.api = api
		acc.botID = botID
		acc.ready = true
		acc.meta.Username = self.Username
		if acc.meta.Phone == "" && self.Phone != "" {
			acc.meta.Phone = "+" + self.Phone
		}
		_ = p.saveMeta(acc.meta)
		p.mu.Unlock()

		log.Printf("[funauth] connected %s", acc.meta.Phone)
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("[funauth] client %s stopped: %v", meta.Phone, err)
	}
	p.mu.Lock()
	if cur := p.accounts[meta.ID]; cur == acc {
		acc.ready = false
		acc.api = nil
	}
	p.mu.Unlock()
}

func (p *funauthPool) handleBotMessage(acc *funauthAccount, e tg.Entities, msgClass tg.MessageClass) error {
	msg, ok := msgClass.(*tg.Message)
	if !ok || msg.Out || msg.Message == "" {
		return nil
	}
	peer, ok := msg.PeerID.(*tg.PeerUser)
	if !ok {
		return nil
	}
	botID := acc.botID
	if botID == 0 {
		if u, ok := e.Users[peer.UserID]; ok && strings.EqualFold(u.Username, funauthBotUser) {
			botID = u.ID
			acc.botID = u.ID
		} else {
			return nil
		}
	}
	if peer.UserID != botID {
		return nil
	}
	select {
	case acc.botMsg <- msg.Message:
	default:
		log.Printf("[funauth] bot msg buffer full for %s", acc.meta.Phone)
	}
	return nil
}

func resolveFunauthBotID(ctx context.Context, api *tg.Client) (int64, error) {
	res, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{
		Username: funauthBotUser,
	})
	if err != nil {
		return 0, err
	}
	for _, u := range res.Users {
		user, ok := u.(*tg.User)
		if ok && strings.EqualFold(user.Username, funauthBotUser) {
			return user.ID, nil
		}
	}
	return 0, errors.New("bot user not in resolve result")
}

func (p *funauthPool) loginStart(phone string) (map[string]any, error) {
	if !p.configured() {
		return nil, errFunauthNotConfigured
	}
	normalized := normalizeFunauthPhone(phone)
	if normalized == "" {
		return nil, errors.New("phone_required")
	}

	p.mu.Lock()
	if prev := p.pending[normalized]; prev != nil {
		prev.cancel()
		delete(p.pending, normalized)
	}
	id := uuid.New().String()
	a := newChannelAuth(normalized)
	ctx, cancel := context.WithCancel(p.ctx)
	pend := &funauthPendingLogin{
		id:     id,
		phone:  normalized,
		auth:   a,
		ctx:    ctx,
		cancel: cancel,
		done:   make(chan funauthLoginResult, 1),
	}
	p.pending[normalized] = pend
	p.mu.Unlock()

	go p.runPendingLogin(pend)

	select {
	case <-a.codeSent:
		return map[string]any{"phone": normalized, "sent": true}, nil
	case res := <-pend.done:
		p.mu.Lock()
		delete(p.pending, normalized)
		p.mu.Unlock()
		if res.err != nil {
			return nil, res.err
		}
		return map[string]any{"phone": normalized, "sent": true, "already": true}, nil
	case <-time.After(60 * time.Second):
		cancel()
		return nil, errors.New("send_code_timeout")
	case <-p.ctx.Done():
		return nil, p.ctx.Err()
	}
}

func (p *funauthPool) runPendingLogin(pend *funauthPendingLogin) {
	dispatcher := tg.NewUpdateDispatcher()
	sessionFile := p.sessionPath(pend.id)
	client := telegram.NewClient(p.apiID, p.apiHash, telegram.Options{
		SessionStorage: &session.FileStorage{Path: sessionFile},
		UpdateHandler:  dispatcher,
	})

	finished := false
	err := client.Run(pend.ctx, func(ctx context.Context) error {
		flow := auth.NewFlow(pend.auth, auth.SendCodeOptions{})
		if err := client.Auth().IfNecessary(ctx, flow); err != nil {
			return err
		}
		self, err := client.Self(ctx)
		if err != nil {
			return err
		}
		username := self.Username
		phone := pend.phone
		if phone == "" && self.Phone != "" {
			phone = "+" + self.Phone
		}

		p.mu.Lock()
		var replaceIDs []string
		for id, acc := range p.accounts {
			if normalizeFunauthPhone(acc.meta.Phone) == phone {
				replaceIDs = append(replaceIDs, id)
			}
		}
		p.mu.Unlock()
		for _, id := range replaceIDs {
			_ = p.remove(id)
		}

		meta := funauthAccountMeta{
			ID:       pend.id,
			Phone:    phone,
			Username: username,
			Full:     false,
			Started:  false,
		}
		if err := p.saveMeta(meta); err != nil {
			return err
		}

		api := client.API()
		botID, _ := resolveFunauthBotID(ctx, api)
		acc := &funauthAccount{
			meta:   meta,
			ready:  true,
			api:    api,
			botID:  botID,
			botMsg: make(chan string, 32),
			cancel: pend.cancel,
		}
		dispatcher.OnNewMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewMessage) error {
			return p.handleBotMessage(acc, e, u.Message)
		})

		p.mu.Lock()
		p.accounts[meta.ID] = acc
		delete(p.pending, pend.phone)
		p.mu.Unlock()

		finished = true
		select {
		case pend.done <- funauthLoginResult{view: acc.view()}:
		default:
		}
		log.Printf("[funauth] login ok %s", phone)
		<-ctx.Done()
		return ctx.Err()
	})

	if !finished {
		res := funauthLoginResult{err: err}
		if err == nil || errors.Is(err, context.Canceled) {
			res.err = errors.New("login_cancelled")
		}
		_ = os.Remove(sessionFile)
		_ = os.Remove(p.metaPath(pend.id))
		select {
		case pend.done <- res:
		default:
		}
		p.mu.Lock()
		if cur := p.pending[pend.phone]; cur == pend {
			delete(p.pending, pend.phone)
		}
		p.mu.Unlock()
	}
}

func (p *funauthPool) loginCode(phone, code string) (map[string]any, error) {
	if !p.configured() {
		return nil, errFunauthNotConfigured
	}
	normalized := normalizeFunauthPhone(phone)
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, errors.New("code_required")
	}

	p.mu.Lock()
	pend := p.pending[normalized]
	p.mu.Unlock()
	if pend == nil {
		return nil, errors.New("login_not_started")
	}

	select {
	case pend.auth.codeCh <- code:
	case <-time.After(5 * time.Second):
		return nil, errors.New("code_channel_busy")
	}

	select {
	case <-pend.auth.needPass:
		return map[string]any{"phone": normalized, "needPassword": true}, nil
	case res := <-pend.done:
		if res.err != nil {
			return nil, res.err
		}
		return map[string]any{
			"id":       res.view.ID,
			"phone":    res.view.Phone,
			"username": res.view.Username,
			"ready":    true,
			"full":     false,
			"started":  false,
		}, nil
	case <-time.After(90 * time.Second):
		return nil, errors.New("login_timeout")
	case <-pend.ctx.Done():
		return nil, errors.New("login_cancelled")
	}
}

func (p *funauthPool) loginPassword(phone, password string) (map[string]any, error) {
	if !p.configured() {
		return nil, errFunauthNotConfigured
	}
	normalized := normalizeFunauthPhone(phone)
	if password == "" {
		return nil, errors.New("password_required")
	}

	p.mu.Lock()
	pend := p.pending[normalized]
	p.mu.Unlock()
	if pend == nil {
		return nil, errors.New("login_not_started")
	}

	select {
	case pend.auth.passCh <- password:
	case <-time.After(5 * time.Second):
		return nil, errors.New("password_channel_busy")
	}

	select {
	case res := <-pend.done:
		if res.err != nil {
			return nil, res.err
		}
		return map[string]any{
			"id":       res.view.ID,
			"phone":    res.view.Phone,
			"username": res.view.Username,
			"ready":    true,
			"full":     false,
			"started":  false,
		}, nil
	case <-time.After(90 * time.Second):
		return nil, errors.New("login_timeout")
	case <-pend.ctx.Done():
		return nil, errors.New("login_cancelled")
	}
}

func (p *funauthPool) remove(id string) error {
	p.mu.Lock()
	acc := p.accounts[id]
	if acc != nil {
		delete(p.accounts, id)
		if acc.cancel != nil {
			acc.cancel()
		}
	}
	p.mu.Unlock()
	_ = os.Remove(p.sessionPath(id))
	_ = os.Remove(p.metaPath(id))
	return nil
}
