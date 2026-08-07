package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gotd/td/crypto"
	"github.com/gotd/td/session"
	"github.com/gotd/td/telegram"
	"github.com/gotd/td/telegram/auth"
	"github.com/gotd/td/telegram/dcs"
	"github.com/gotd/td/tg"
	"golang.org/x/net/proxy"
)

const (
	funauthSessionsDir = "funauth_sessions"
	funauthBotUser     = "FunAuthBot"
	funauthChannel     = "funtime"
	funauthNicksFile   = "nicks.json"
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
	nicks    map[string]string // nick(lower) → account id

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
		nicks:    make(map[string]string),
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
	if proxyURL := resolveTelegramProxyURL(); proxyURL != "" {
		log.Printf("[funauth] MTProto через прокси %s", proxyURL)
	} else {
		log.Printf("[funauth] MTProto без прокси (TELEGRAM_PROXY=off)")
	}
	p.loadNicks()
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
		if e.Name() == funauthNicksFile {
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
		metaCopy := meta
		goSafe("funauth:connect:"+meta.ID, func() {
			p.connectAccount(metaCopy)
		})
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

func (p *funauthPool) nicksPath() string {
	return filepath.Join(p.dir, funauthNicksFile)
}

func (p *funauthPool) loadNicks() {
	raw, err := os.ReadFile(p.nicksPath())
	if err != nil {
		return
	}
	var m map[string]string
	if err := json.Unmarshal(raw, &m); err != nil {
		log.Printf("[funauth] bad %s: %v", funauthNicksFile, err)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, v := range m {
		key := strings.ToLower(strings.TrimSpace(k))
		if key == "" || v == "" {
			continue
		}
		p.nicks[key] = v
	}
	log.Printf("[funauth] nick map: %d", len(p.nicks))
}

func (p *funauthPool) rememberNick(nick, accountID string) {
	key := strings.ToLower(strings.TrimSpace(nick))
	id := strings.TrimSpace(accountID)
	if key == "" || id == "" {
		return
	}
	p.mu.Lock()
	p.nicks[key] = id
	snapshot := make(map[string]string, len(p.nicks))
	for k, v := range p.nicks {
		snapshot[k] = v
	}
	p.mu.Unlock()
	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return
	}
	if err := os.WriteFile(p.nicksPath(), raw, 0o600); err != nil {
		log.Printf("[funauth] save nicks: %v", err)
	}
}

func (p *funauthPool) accountByID(id string) *funauthAccount {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.accounts[id]
}

func (p *funauthPool) accountForNick(nick string) *funauthAccount {
	key := strings.ToLower(strings.TrimSpace(nick))
	p.mu.Lock()
	id := p.nicks[key]
	acc := p.accounts[id]
	p.mu.Unlock()
	if acc == nil || !acc.ready || acc.api == nil {
		return nil
	}
	return acc
}

func (p *funauthPool) listReadyAccounts() []*funauthAccount {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]*funauthAccount, 0, len(p.accounts))
	for _, a := range p.accounts {
		if a.ready && !a.meta.Full && a.api != nil {
			out = append(out, a)
		}
	}
	return out
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

// funauthClientOptions — сессия + апдейты + SOCKS как у Bot API (127.0.0.1:1080).
func funauthClientOptions(sessionPath string, handler telegram.UpdateHandler) telegram.Options {
	opt := telegram.Options{
		SessionStorage: &session.FileStorage{Path: sessionPath},
		UpdateHandler:  handler,
	}
	proxyURL := resolveTelegramProxyURL()
	if proxyURL == "" {
		return opt
	}
	host, port := parseSocksHostPort(proxyURL)
	sock5, err := proxy.SOCKS5("tcp", net.JoinHostPort(host, port), nil, proxy.Direct)
	if err != nil {
		log.Printf("[funauth] SOCKS5: %v — без прокси", err)
		return opt
	}
	cd, ok := sock5.(proxy.ContextDialer)
	if !ok {
		log.Printf("[funauth] SOCKS dialer без ContextDialer — без прокси")
		return opt
	}
	opt.Resolver = dcs.Plain(dcs.PlainOptions{Dial: cd.DialContext})
	return opt
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

	client := telegram.NewClient(p.apiID, p.apiHash, funauthClientOptions(p.sessionPath(meta.ID), dispatcher))
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

	goSafe("funauth:login:"+normalized, func() {
		p.runPendingLogin(pend)
	})

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
	client := telegram.NewClient(p.apiID, p.apiHash, funauthClientOptions(sessionFile, dispatcher))

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

// Production DC endpoints (Telethon defaults).
var funauthDCAddr = map[int]string{
	1: "149.154.175.53:443",
	2: "149.154.167.51:443",
	3: "149.154.175.100:443",
	4: "149.154.167.91:443",
	5: "91.108.56.165:443",
}

func parseFunauthSessionInput(raw string, dcID int) (*session.Data, error) {
	s := strings.TrimSpace(raw)
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, " ", "")
	if s == "" {
		return nil, errors.New("authkey_required")
	}
	if strings.Contains(s, "...") {
		return nil, errors.New("authkey_truncated: вставь полный ключ без «...»")
	}

	// dc:hexauthkey
	if i := strings.IndexByte(s, ':'); i > 0 && i < 3 {
		prefix := s[:i]
		rest := s[i+1:]
		if n, err := strconv.Atoi(prefix); err == nil && n >= 1 && n <= 5 {
			dcID = n
			s = rest
		}
	}

	// Telethon StringSession: starts with '1' and is not pure hex of length 512.
	if len(s) > 1 && s[0] == '1' {
		hexOnly := true
		for _, c := range s {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
				hexOnly = false
				break
			}
		}
		if !hexOnly || len(s) != 513 {
			data, err := session.TelethonSession(s)
			if err != nil {
				return nil, fmt.Errorf("telethon_session: %w", err)
			}
			return data, nil
		}
	}

	hexStr := strings.Builder{}
	for _, c := range s {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			hexStr.WriteRune(c)
		}
	}
	h := hexStr.String()
	if len(h) != 512 {
		return nil, fmt.Errorf("authkey_len: нужно 512 hex-символов (256 байт), сейчас %d", len(h))
	}
	keyBytes, err := hex.DecodeString(h)
	if err != nil {
		return nil, errors.New("authkey_hex_invalid")
	}
	if dcID < 1 || dcID > 5 {
		dcID = 2
	}
	addr, ok := funauthDCAddr[dcID]
	if !ok {
		return nil, errors.New("dc_invalid")
	}
	var key crypto.Key
	copy(key[:], keyBytes)
	id := key.WithID().ID
	return &session.Data{
		DC:        dcID,
		Addr:      addr,
		AuthKey:   key[:],
		AuthKeyID: id[:],
	}, nil
}

// importAuthKey — добавить аккаунт из hex auth_key или Telethon StringSession.
func (p *funauthPool) importAuthKey(raw string, dcID int) (funauthAccountView, error) {
	if !p.configured() {
		return funauthAccountView{}, errFunauthNotConfigured
	}
	data, err := parseFunauthSessionInput(raw, dcID)
	if err != nil {
		return funauthAccountView{}, err
	}

	id := uuid.New().String()
	sessionFile := p.sessionPath(id)
	loader := session.Loader{Storage: &session.FileStorage{Path: sessionFile}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := loader.Save(ctx, data); err != nil {
		return funauthAccountView{}, fmt.Errorf("session_save: %w", err)
	}

	meta := funauthAccountMeta{
		ID:    id,
		Phone: "authkey:" + id[:8],
	}
	if err := p.saveMeta(meta); err != nil {
		_ = os.Remove(sessionFile)
		return funauthAccountView{}, err
	}

	goSafe("funauth:connect:"+id, func() {
		p.connectAccount(meta)
	})

	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		acc := p.accounts[id]
		ready := acc != nil && acc.ready
		var view funauthAccountView
		if ready {
			view = acc.view()
		}
		p.mu.Unlock()
		if ready {
			log.Printf("[funauth] authkey import ok %s (%s)", view.Phone, id[:8])
			return view, nil
		}
		time.Sleep(200 * time.Millisecond)
	}

	_ = p.remove(id)
	return funauthAccountView{}, errors.New("authkey_connect_timeout")
}

