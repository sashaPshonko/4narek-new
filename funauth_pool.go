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
	"regexp"
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
	funauthSessionsDir  = "funauth_sessions"
	funauthBotUser      = "FunAuthBot"
	funauthChannel      = "funtime"
	funauthNicksFile    = "nicks.json"
	funauthVerifiedFile = "verified.json"
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
	// FullBot — FunAuthBot сам отказал («много аккаунтов»). Full считается как FullBot или «ботов фермы на TG уже лимит».
	FullBot bool `json:"fullBot,omitempty"`
	Started bool `json:"started"`
	Anarchy int  `json:"anarchy,omitempty"`
}

type funauthAccountView struct {
	ID          string `json:"id"`
	Phone       string `json:"phone"`
	Username    string `json:"username"`
	Ready       bool   `json:"ready"`
	Full        bool   `json:"full"`
	Started     bool   `json:"started"`
	Anarchy     int    `json:"anarchy,omitempty"`
	RosterBound int    `json:"rosterBound,omitempty"`
	RosterTotal int    `json:"rosterTotal,omitempty"`
}

type funauthAccount struct {
	meta     funauthAccountMeta
	ready    bool
	api      *tg.Client
	botID    int64
	botMsg   chan string
	cancel   context.CancelFunc
	socksURL string
	// floodUntil — Telegram вернул FLOOD_WAIT: до этого момента аккаунт для биндов не берём.
	floodUntil time.Time
	// claimedBy — ник, чей bind сейчас идёт на этом TG (под p.mu). Пока занят — другим бинд-задачам не выдаём:
	// параллельные задачи на одном TG сливают очередь botMsg и воруют ответы друг друга.
	claimedBy string
	// jobMu — одна команда FunAuthBot (bind/2fa) на TG за раз.
	jobMu sync.Mutex
	// unconfirmed — подряд ответы «не дождались» на этом TG (под p.mu). 2 подряд → считаем TG мёртвым.
	unconfirmed int
	// stopped — клиент MTProto завершился (обрыв, markDead). Проверка пула поднимет его заново.
	stopped bool
}

// releaseClaim — бинд на TG закончен, акк снова можно выдавать.
func (p *funauthPool) releaseClaim(acc *funauthAccount) {
	if p == nil || acc == nil {
		return
	}
	p.mu.Lock()
	acc.claimedBy = ""
	p.mu.Unlock()
}

// noteUnconfirmed — ответ FunAuthBot не пришёл (привязка/2FA под вопросом). Возвращает сколько раз подряд.
func (p *funauthPool) noteUnconfirmed(acc *funauthAccount) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	acc.unconfirmed++
	return acc.unconfirmed
}

func (p *funauthPool) resetUnconfirmed(acc *funauthAccount) {
	p.mu.Lock()
	acc.unconfirmed = 0
	p.mu.Unlock()
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
	sentType     string // app / sms / call / …

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

func describeSentCodeType(t tg.AuthSentCodeTypeClass) string {
	switch t.(type) {
	case *tg.AuthSentCodeTypeApp:
		return "app"
	case *tg.AuthSentCodeTypeSMS:
		return "sms"
	case *tg.AuthSentCodeTypeCall:
		return "call"
	case *tg.AuthSentCodeTypeFlashCall:
		return "flash_call"
	case *tg.AuthSentCodeTypeMissedCall:
		return "missed_call"
	case *tg.AuthSentCodeTypeEmailCode:
		return "email"
	case *tg.AuthSentCodeTypeFragmentSMS:
		return "fragment_sms"
	case *tg.AuthSentCodeTypeFirebaseSMS:
		return "firebase_sms"
	case *tg.AuthSentCodeTypeSMSWord:
		return "sms_word"
	case *tg.AuthSentCodeTypeSMSPhrase:
		return "sms_phrase"
	default:
		if t == nil {
			return "unknown"
		}
		return fmt.Sprintf("%T", t)
	}
}

func (a *channelAuth) Phone(context.Context) (string, error) { return a.phone, nil }

func (a *channelAuth) Code(ctx context.Context, sent *tg.AuthSentCode) (string, error) {
	if sent != nil {
		a.sentType = describeSentCodeType(sent.Type)
		log.Printf("[funauth] code sent to %s via %s (timeout=%ds)", a.phone, a.sentType, sent.Timeout)
	}
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
	verified map[string]bool   // nick(lower) → game verified (5s on anarchy без «чтобы двигаться»)
	// twofaPending — ники, у которых 2FA не подтверждена (twofa_pending.json). TG с такими никами не удаляем.
	twofaPending map[string]bool
	roster       funauthRoster
	// perAccount — сколько ботов фермы можно привязать к одному TG-аккаунту (1..funauthMaxPerAccount, задаётся в панели).
	perAccount int

	ctx    context.Context
	cancel context.CancelFunc
}

func newFunauthPool() *funauthPool {
	ctx, cancel := context.WithCancel(context.Background())
	return &funauthPool{
		apiID:      funauthDesktopAPIID,
		apiHash:    funauthDesktopAPIHash,
		dir:        funauthSessionsDir,
		accounts:   make(map[string]*funauthAccount),
		pending:    make(map[string]*funauthPendingLogin),
		nicks:      make(map[string]string),
		verified:   make(map[string]bool),
		perAccount: 1,

		twofaPending: make(map[string]bool),
		ctx:          ctx,
		cancel:       cancel,
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
		log.Printf("[funauth] MTProto напрямую (bind-прокси из пула — если есть рабочий)")
	}
	p.loadSettings()
	p.loadNicks()
	p.loadVerified()
	p.loadTwoFAPending()
	funauthBindProxies.init(p.dir)
	p.roster = loadFleetRunningNicks()
	p.cleanupOrphanAnarchies()
	p.syncAllRosterFull()
	entries, err := os.ReadDir(p.dir)
	if err != nil {
		log.Printf("[funauth] read sessions: %v", err)
		return
	}
	n := 0
	phones := map[string]string{} // id → телефон из мета-файлов (для журнала TG ↔ бот)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		if e.Name() == funauthNicksFile || e.Name() == funauthVerifiedFile || e.Name() == funauthBindProxiesFile ||
			e.Name() == funauthTwoFAPendingFile {
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
		phones[meta.ID] = meta.Phone
		if _, err := os.Stat(p.sessionPath(meta.ID)); err != nil {
			log.Printf("[funauth] session missing for %s", meta.ID)
			continue
		}
		n++
		metaCopy := meta
		goSafe("funauth:connect:"+meta.ID, func() {
			p.connectAccount(metaCopy, pickFarmLoginSOCKS())
		})
	}
	log.Printf("[funauth] loading %d account(s) from %s", n, p.dir)
	p.backfillTGMap(phones)
	p.scheduleScrubBound()
	p.startHealthLoop()
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

func (p *funauthPool) verifiedPath() string {
	return filepath.Join(p.dir, funauthVerifiedFile)
}

func (p *funauthPool) loadVerified() {
	raw, err := os.ReadFile(p.verifiedPath())
	if err != nil {
		return
	}
	var m map[string]bool
	if err := json.Unmarshal(raw, &m); err != nil {
		log.Printf("[funauth] bad %s: %v", funauthVerifiedFile, err)
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for k, v := range m {
		key := strings.ToLower(strings.TrimSpace(k))
		if key == "" || !v {
			continue
		}
		p.verified[key] = true
	}
	log.Printf("[funauth] verified map: %d", len(p.verified))
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
	p.syncAccountRosterFull(id)
}

func (p *funauthPool) markFull(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	a := p.accounts[id]
	if a == nil {
		return
	}
	a.meta.Full = true
	a.meta.FullBot = true
	if err := p.saveMeta(a.meta); err != nil {
		log.Printf("[funauth] save meta %s: %v", id, err)
	}
	log.Printf("[funauth] TG %s → full (FunAuthBot: много аккаунтов)", a.meta.Phone)
}

// markDead — сессия MTProto дохлая (CONNECTION_LAYER_INVALID и т.п.): убираем из ready и рвём клиент,
// биндер берёт следующий акк.
func (p *funauthPool) markDead(id, reason string) {
	if p == nil || strings.TrimSpace(id) == "" {
		return
	}
	p.mu.Lock()
	a := p.accounts[id]
	if a == nil {
		p.mu.Unlock()
		return
	}
	phone := a.meta.Phone
	a.ready = false
	a.api = nil
	cancel := a.cancel
	a.cancel = nil
	p.mu.Unlock()
	// Ники за TG не стираем: 2FA у них выключается только с этого TG, проверка пула его переподключит.
	if cancel != nil {
		cancel()
	}
	reason = strings.TrimSpace(reason)
	if len(reason) > 120 {
		reason = reason[:120]
	}
	log.Printf("[funauth] TG %s → dead (%s), переподключу при проверке пула", phone, reason)
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

func (p *funauthPool) nickBound(nick string) bool {
	key := strings.ToLower(strings.TrimSpace(nick))
	if key == "" {
		return false
	}
	p.mu.Lock()
	_, ok := p.nicks[key]
	p.mu.Unlock()
	return ok
}

func (p *funauthPool) nickVerified(nick string) bool {
	key := strings.ToLower(strings.TrimSpace(nick))
	if key == "" {
		return false
	}
	p.mu.Lock()
	ok := p.verified[key]
	p.mu.Unlock()
	return ok
}

func (p *funauthPool) forgetVerified(nick string) bool {
	key := strings.ToLower(strings.TrimSpace(nick))
	if key == "" {
		return false
	}
	p.mu.Lock()
	if !p.verified[key] {
		p.mu.Unlock()
		return false
	}
	delete(p.verified, key)
	snapshot := make(map[string]bool, len(p.verified))
	for k, v := range p.verified {
		if v {
			snapshot[k] = true
		}
	}
	p.mu.Unlock()
	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return true
	}
	if err := os.WriteFile(p.verifiedPath(), raw, 0o600); err != nil {
		log.Printf("[funauth] save verified: %v", err)
	}
	log.Printf("[funauth] forgot game-verified %s (нужен bind)", nick)
	return true
}

func (p *funauthPool) rememberVerified(nick string, anarchy int) bool {
	key := strings.ToLower(strings.TrimSpace(nick))
	if key == "" {
		return false
	}
	if anarchy <= 0 {
		anarchy = p.rosterAnarchy(key)
	}
	p.mu.Lock()
	if p.verified[key] {
		p.mu.Unlock()
		return false
	}
	p.verified[key] = true
	snapshot := make(map[string]bool, len(p.verified))
	for k, v := range p.verified {
		if v {
			snapshot[k] = true
		}
	}
	p.mu.Unlock()
	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return true
	}
	if err := os.WriteFile(p.verifiedPath(), raw, 0o600); err != nil {
		log.Printf("[funauth] save verified: %v", err)
	}
	log.Printf("[funauth] game verified %s an%d", nick, anarchy)
	p.syncAnarchyRosterFull(anarchy)
	return true
}

func (p *funauthPool) syncAnarchyRosterFull(anarchy int) {
	if anarchy <= 0 {
		return
	}
	p.mu.Lock()
	idSet := make(map[string]struct{})
	for id, acc := range p.accounts {
		if acc.meta.Anarchy == anarchy {
			idSet[id] = struct{}{}
		}
	}
	for nick, id := range p.nicks {
		if p.roster.anarchyForNick(nick) == anarchy {
			idSet[id] = struct{}{}
		}
	}
	p.mu.Unlock()
	for id := range idSet {
		p.syncAccountRosterFull(id)
	}
}

// syncAccountRosterFull — TG закрыт, когда к нему привязано ботов фермы столько, сколько разрешено в панели
// (по умолчанию 1 MC = 1 TG), или когда FunAuthBot сам отказал. Овнер слот не закрывает (следующий овнер той же анки).
func (p *funauthPool) syncAccountRosterFull(accountID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	acc := p.accounts[accountID]
	if acc == nil {
		return false
	}
	// старые мета-файлы без fullBot: Full там мог быть и от лимита 1, и от отказа бота — пересчитываем, отказ бот повторит
	complete := acc.meta.FullBot || p.accountFarmCountLocked(accountID) >= p.perAccountLocked()
	if acc.meta.Full != complete {
		was := acc.meta.Full
		acc.meta.Full = complete
		if err := p.saveMeta(acc.meta); err != nil {
			log.Printf("[funauth] save meta %s: %v", accountID, err)
		}
		if complete {
			log.Printf("[funauth] TG %s → full (ботов фермы: лимит %d)", accountID, p.perAccountLocked())
		} else if was {
			log.Printf("[funauth] TG %s → not full", accountID)
		}
	}
	return acc.meta.Full
}

func (p *funauthPool) syncAllRosterFull() {
	p.mu.Lock()
	ids := make([]string, 0, len(p.accounts))
	for id := range p.accounts {
		ids = append(ids, id)
	}
	p.mu.Unlock()
	for _, id := range ids {
		p.syncAccountRosterFull(id)
	}
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
		v := a.view()
		an := a.meta.Anarchy
		if an > 0 {
			bound := p.accountBoundCountLocked(a.meta.ID)
			if bound > 1 {
				bound = 1
			}
			v.RosterBound = bound
			v.RosterTotal = 1
		}
		out = append(out, v)
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
		Anarchy:  a.meta.Anarchy,
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

func (p *funauthPool) rosterStats() (bound, total int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.roster == nil {
		return 0, 0
	}
	return p.roster.globalProgress(p.nicks, p.verified)
}

func (p *funauthPool) replaceRoster(r funauthRoster) {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.roster = r
	p.mu.Unlock()
}

func (p *funauthPool) rosterAnarchy(nick string) int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	r := p.roster
	p.mu.Unlock()
	return r.anarchyForNick(nick)
}

func (p *funauthPool) accountBoundCountLocked(accountID string) int {
	n := 0
	for _, id := range p.nicks {
		if id == accountID {
			n++
		}
	}
	return n
}

func (p *funauthPool) accountHasFarmNickLocked(accountID string) bool {
	for nick, id := range p.nicks {
		if id == accountID && nickIsFarmBot(nick) {
			return true
		}
	}
	return false
}

// accountFarmCountLocked — сколько ботов фермы привязано к TG.
func (p *funauthPool) accountFarmCountLocked(accountID string) int {
	n := 0
	for nick, id := range p.nicks {
		if id == accountID && nickIsFarmBot(nick) {
			n++
		}
	}
	return n
}

// accountHasOwnerNickLocked — среди привязанных к TG есть овнер клана (овнеров с ботами фермы не смешиваем).
func (p *funauthPool) accountHasOwnerNickLocked(accountID string) bool {
	owners := currentClanOwnerRoster()
	if len(owners) == 0 {
		return false
	}
	for nick, id := range p.nicks {
		if id != accountID {
			continue
		}
		for _, set := range owners {
			if _, ok := set[nick]; ok {
				return true
			}
		}
	}
	return false
}

func (p *funauthPool) cleanupOrphanAnarchies() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for id, acc := range p.accounts {
		if acc.meta.Anarchy == 0 {
			continue
		}
		if p.accountBoundCountLocked(id) > 0 {
			continue
		}
		log.Printf("[funauth] clear orphan anarchy %d on %s (0 binds)", acc.meta.Anarchy, acc.meta.Phone)
		acc.meta.Anarchy = 0
		acc.meta.Full = false
		if err := p.saveMeta(acc.meta); err != nil {
			log.Printf("[funauth] save meta %s: %v", id, err)
		}
	}
}

type funauthPickDiag struct {
	Offline  int
	Full     int
	Busy     int // TG уже привязан к другому MC-нику
	Excluded int
	// OtherAn — устар.; для логов совместимости = Busy
	OtherAn int
}

func (p *funauthPool) pickForAnarchyBindDiag(
	nick string,
	anarchy int,
	exclude map[string]struct{},
) (*funauthAccount, funauthPickDiag) {
	key := strings.ToLower(strings.TrimSpace(nick))

	p.mu.Lock()
	defer p.mu.Unlock()

	var diag funauthPickDiag
	var free []*funauthAccount
	var ownerReuse []*funauthAccount
	type sharedCand struct {
		acc   *funauthAccount
		bound int
	}
	var shared []sharedCand
	limit := p.perAccountLocked()
	ownerJob := nickIsClanOwnerOf(key, anarchy)

	// TG-логины в процессе (loginStart → loginCode/importAuthKey → connectAccount)
	// ещё не в p.accounts, но скоро появятся. Считаем их busy, чтобы биндер
	// подождал (processJob: if diag.Busy > 0 → sleep 5s → continue).
	diag.Busy += len(p.pending)

	for _, acc := range p.accounts {
		if _, skip := exclude[acc.meta.ID]; skip {
			diag.Excluded++
			continue
		}
		if !acc.ready || acc.api == nil {
			diag.Offline++
			continue
		}
		if time.Now().Before(acc.floodUntil) {
			diag.Offline++
			continue
		}
		if acc.meta.Full {
			diag.Full++
			continue
		}
		if acc.claimedBy != "" && acc.claimedBy != key {
			diag.Busy++
			continue
		}
		if key != "" && p.nicks[key] == acc.meta.ID {
			acc.claimedBy = key
			return acc, diag
		}
		if bound := p.accountBoundCountLocked(acc.meta.ID); bound > 0 {
			if ownerJob && !p.accountHasFarmNickLocked(acc.meta.ID) &&
				(acc.meta.Anarchy == 0 || acc.meta.Anarchy == anarchy) {
				ownerReuse = append(ownerReuse, acc)
				continue
			}
			// лимит ботов на один TG из панели: добираем TG, где ещё есть место (овнеров с ботами фермы не смешиваем).
			// Считаем БОТОВ ФЕРМЫ, как и пометка «заполнен» (syncAccountRosterFull): иначе старые (уже заменённые)
			// ники, оставшиеся в nicks.json, занимают места, TG не закрывается и не удаляется, а боты бесконечно
			// ждут «busy». Если ботов фермы на TG нет совсем (чужие ники), считаем по всем — осторожнее.
			used := p.accountFarmCountLocked(acc.meta.ID)
			if used == 0 {
				used = bound
			}
			if !ownerJob && used < limit && !p.accountHasOwnerNickLocked(acc.meta.ID) {
				shared = append(shared, sharedCand{acc: acc, bound: used})
				continue
			}
			diag.Busy++
			diag.OtherAn = diag.Busy
			continue
		}
		free = append(free, acc)
	}

	if len(ownerReuse) > 0 {
		ownerReuse[0].claimedBy = key
		return ownerReuse[0], diag
	}
	if len(shared) > 0 {
		// сначала заполняем самый заполненный TG, чтобы остальные оставались свободными; при равенстве — по номеру
		best := shared[0]
		for _, c := range shared[1:] {
			if c.bound > best.bound || (c.bound == best.bound && c.acc.meta.Phone < best.acc.meta.Phone) {
				best = c
			}
		}
		best.acc.claimedBy = key
		return best.acc, diag
	}
	if len(free) == 0 {
		return nil, diag
	}
	free[0].claimedBy = key
	return free[0], diag
}

func (p *funauthPool) assignAnarchy(accountID string, anarchy int) {
	if anarchy <= 0 || accountID == "" {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	acc := p.accounts[accountID]
	if acc == nil {
		return
	}
	if acc.meta.Anarchy == 0 {
		acc.meta.Anarchy = anarchy
		if err := p.saveMeta(acc.meta); err != nil {
			log.Printf("[funauth] save meta %s: %v", accountID, err)
		}
		log.Printf("[funauth] TG %s → anarchy %d", acc.meta.Phone, anarchy)
	} else if acc.meta.Anarchy != anarchy && p.perAccountLocked() <= 1 {
		log.Printf("[funauth] WARN TG %s anarchy %d, bind for %d", acc.meta.Phone, acc.meta.Anarchy, anarchy)
	}
}

func (p *funauthPool) afterBindSuccess(nick, accountID string, anarchy int) {
	nickKey := strings.ToLower(strings.TrimSpace(nick))
	if anarchy <= 0 && nickKey != "" {
		anarchy = p.rosterAnarchy(nickKey)
	}
	p.assignAnarchy(accountID, anarchy)
	p.rememberNick(nick, accountID)
	p.logTGMap(nick, accountID, anarchy)
	log.Printf("[funauth] bind %s → TG %s (an%d)", nick, accountID, anarchy)
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

// funauthClientOptions — сессия + апдейты + SOCKS. socksURL пустой → TELEGRAM_PROXY, если задан, иначе напрямую.
func funauthClientOptions(sessionPath string, handler telegram.UpdateHandler, socksURL string) telegram.Options {
	opt := telegram.Options{
		SessionStorage: &session.FileStorage{Path: sessionPath},
		UpdateHandler:  handler,
	}
	proxyURL := strings.TrimSpace(socksURL)
	if proxyURL == "" {
		proxyURL = resolveTelegramProxyURL()
	}
	if proxyURL == "" {
		return opt
	}
	cd, err := socks5ContextDialer(proxyURL)
	if err != nil {
		log.Printf("[funauth] SOCKS5: %v — без прокси", err)
		return opt
	}
	opt.Resolver = dcs.Plain(dcs.PlainOptions{
		Network:    "tcp4",
		PreferIPv6: false,
		Dial:       funauthDialIPv4(cd),
	})
	return opt
}

// funauthDialIPv4 — Алматы без IPv6; gotd иначе шлёт migrate на 2001:… и ловит deadline.
func funauthDialIPv4(cd proxy.ContextDialer) dcs.DialFunc {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if ip := net.ParseIP(host); ip != nil && ip.To4() == nil {
			return nil, fmt.Errorf("skip ipv6 dc %s", host)
		}
		if network == "tcp" || network == "tcp6" {
			network = "tcp4"
		}
		return cd.DialContext(ctx, network, net.JoinHostPort(host, port))
	}
}

func (p *funauthPool) connectAccount(meta funauthAccountMeta, socksURL string) {
	dispatcher := tg.NewUpdateDispatcher()
	acc := &funauthAccount{
		meta:     meta,
		botMsg:   make(chan string, 32),
		socksURL: strings.TrimSpace(socksURL),
	}
	dispatcher.OnNewMessage(func(ctx context.Context, e tg.Entities, u *tg.UpdateNewMessage) error {
		return p.handleBotMessage(acc, e, u.Message)
	})

	client := telegram.NewClient(p.apiID, p.apiHash, funauthClientOptions(p.sessionPath(meta.ID), dispatcher, socksURL))
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

		via := "direct"
		if acc.socksURL != "" {
			via = socksURLHost(acc.socksURL)
		}
		log.Printf("[funauth] connected %s via %s", acc.meta.Phone, via)
		if p.accountShouldScrub(acc) {
			phone := acc.meta.Phone
			goSafe("funauth:scrub-on-connect:"+acc.meta.ID, func() {
				time.Sleep(2 * time.Second)
				sctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				defer cancel()
				if err := funauthScrubBotChat(sctx, acc); err != nil {
					log.Printf("[funauth] scrub on connect %s: %v", phone, err)
				}
			})
		}
		<-ctx.Done()
		return ctx.Err()
	})
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("[funauth] client %s stopped: %v", meta.Phone, err)
	}
	p.mu.Lock()
	current := p.accounts[meta.ID] == acc
	if current {
		acc.ready = false
		acc.api = nil
		acc.stopped = true
	}
	p.mu.Unlock()
	// сессии больше нет (отозвана, акк удалён) — переподключаться бесполезно
	if current && err != nil && (funauthNoAccessErr(err) || strings.Contains(err.Error(), "not authorized")) {
		p.dropAccount(meta.ID, err.Error())
	}
}

// ensureNickSOCKS переподключает TG-акк через рабочий SOCKS из bind_proxies.json (не ip.json ботов).
func (p *funauthPool) ensureNickSOCKS(acc *funauthAccount, nick string) *funauthAccount {
	if p == nil || acc == nil {
		return acc
	}
	socks := pickBindSOCKS()
	if socks == "" {
		log.Printf("[funauth] %s: нет рабочего bind-прокси — напрямую", nick)
		return acc
	}
	p.mu.Lock()
	same := acc.ready && acc.socksURL == socks && acc.api != nil
	meta := acc.meta
	p.mu.Unlock()
	if same {
		log.Printf("[funauth] %s: уже %s", nick, socksURLHost(socks))
		return acc
	}
	log.Printf("[funauth] %s: MTProto → %s", nick, socksURLHost(socks))
	if !p.reconnectAccount(meta, socks) {
		log.Printf("[funauth] %s: не поднялись через bind-прокси — напрямую", nick)
		p.mu.Lock()
		cur := p.accounts[meta.ID]
		p.mu.Unlock()
		if cur != nil {
			return cur
		}
		return acc
	}
	p.mu.Lock()
	cur := p.accounts[meta.ID]
	p.mu.Unlock()
	if cur != nil {
		return cur
	}
	return acc
}

func (p *funauthPool) reconnectAccount(meta funauthAccountMeta, socksURL string) bool {
	p.mu.Lock()
	old := p.accounts[meta.ID]
	p.mu.Unlock()
	if old != nil && old.cancel != nil {
		old.cancel()
	}
	waitDead := time.Now().Add(20 * time.Second)
	for time.Now().Before(waitDead) {
		p.mu.Lock()
		cur := p.accounts[meta.ID]
		alive := cur != nil && cur.ready && (old == nil || cur == old)
		p.mu.Unlock()
		if !alive {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	goSafe("funauth:reconnect:"+meta.ID, func() {
		p.connectAccount(meta, socksURL)
	})
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		cur := p.accounts[meta.ID]
		ok := cur != nil && cur.ready && cur.api != nil && cur.socksURL == socksURL
		p.mu.Unlock()
		if ok {
			return true
		}
		time.Sleep(200 * time.Millisecond)
	}
	return false
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
		out := map[string]any{"phone": normalized, "sent": true}
		if a.sentType != "" {
			out["via"] = a.sentType
		}
		return out, nil
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
	socks := pickFarmLoginSOCKS()
	if socks != "" {
		log.Printf("[funauth] login %s via %s", pend.phone, socksURLHost(socks))
	} else {
		log.Printf("[funauth] login %s — напрямую (нет рабочего bind-прокси)", pend.phone)
	}
	client := telegram.NewClient(p.apiID, p.apiHash, funauthClientOptions(sessionFile, dispatcher, socks))

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
			meta:     meta,
			ready:    true,
			api:      api,
			botID:    botID,
			botMsg:   make(chan string, 32),
			cancel:   pend.cancel,
			socksURL: socks,
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
		via := "direct"
		if socks != "" {
			via = socksURLHost(socks)
		}
		log.Printf("[funauth] login ok %s via %s", phone, via)
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

// Параллельных импортов authkey (каждый ещё крутит DC-probe).
var funauthImportSem = make(chan struct{}, 4)

type funauthParsedAuth struct {
	// Telethon / готовая сессия с известным DC.
	Data *session.Data
	// Сырой auth_key (256 байт) — DC подбираем или берём ForcedDC.
	Key []byte
	// 1..5 зафиксирован; 0 = автоперебор.
	ForcedDC int
}

func funauthDCProbeOrder(forcedDC int) []int {
	if forcedDC >= 1 && forcedDC <= 5 {
		return []int{forcedDC}
	}
	// US часто 1/5, EU — 2/4.
	return []int{1, 2, 5, 4, 3}
}

func sessionDataFromAuthKey(keyBytes []byte, dcID int) (*session.Data, error) {
	if len(keyBytes) != 256 {
		return nil, errors.New("authkey_len")
	}
	if dcID < 1 || dcID > 5 {
		return nil, errors.New("dc_invalid")
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

func parseFunauthSessionInput(raw string, dcID int) (funauthParsedAuth, error) {
	s := strings.TrimSpace(raw)
	s = strings.ReplaceAll(s, "\n", "")
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, " ", "")
	if s == "" {
		return funauthParsedAuth{}, errors.New("authkey_required")
	}
	if strings.Contains(s, "...") {
		return funauthParsedAuth{}, errors.New("authkey_truncated: вставь полный ключ без «...»")
	}

	forced := dcID
	// dc:hexauthkey
	if i := strings.IndexByte(s, ':'); i > 0 && i < 3 {
		prefix := s[:i]
		rest := s[i+1:]
		if n, err := strconv.Atoi(prefix); err == nil && n >= 1 && n <= 5 {
			forced = n
			s = rest
		}
	}

	hexStr := strings.Builder{}
	for _, c := range s {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F') {
			hexStr.WriteRune(c)
		}
	}
	h := hexStr.String()
	// Чистый 256-байтный auth_key (512 hex). Может начинаться с '1' — это НЕ Telethon.
	if len(h) == 512 && len(h) == len(s) {
		keyBytes, err := hex.DecodeString(h)
		if err != nil {
			return funauthParsedAuth{}, errors.New("authkey_hex_invalid")
		}
		return funauthParsedAuth{Key: keyBytes, ForcedDC: forced}, nil
	}

	// Telethon StringSession (обычно base64 после версии '1', не чистый hex512).
	if len(s) > 1 && s[0] == '1' {
		data, err := session.TelethonSession(s)
		if err != nil {
			return funauthParsedAuth{}, fmt.Errorf("telethon_session: %w", err)
		}
		return funauthParsedAuth{Data: data, ForcedDC: data.DC}, nil
	}

	if len(h) != 512 {
		return funauthParsedAuth{}, fmt.Errorf("authkey_len: нужно 512 hex-символов (256 байт), сейчас %d", len(h))
	}
	keyBytes, err := hex.DecodeString(h)
	if err != nil {
		return funauthParsedAuth{}, errors.New("authkey_hex_invalid")
	}
	return funauthParsedAuth{Key: keyBytes, ForcedDC: forced}, nil
}

func (p *funauthPool) saveSessionData(accountID string, data *session.Data) error {
	loader := session.Loader{Storage: &session.FileStorage{Path: p.sessionPath(accountID)}}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return loader.Save(ctx, data)
}

func (p *funauthPool) stopAccountClient(accountID string) {
	p.mu.Lock()
	acc := p.accounts[accountID]
	if acc != nil && acc.cancel != nil {
		acc.cancel()
	}
	p.mu.Unlock()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		acc := p.accounts[accountID]
		alive := acc != nil && acc.api != nil
		p.mu.Unlock()
		if !alive {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (p *funauthPool) waitAccountReady(accountID string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		acc := p.accounts[accountID]
		ready := acc != nil && acc.ready && acc.api != nil
		p.mu.Unlock()
		if ready {
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}

func (p *funauthPool) connectAuthKeyWithDCProbe(meta funauthAccountMeta, key []byte, forcedDC int) {
	socks := pickFarmLoginSOCKS()
	order := funauthDCProbeOrder(forcedDC)
	for _, dc := range order {
		data, err := sessionDataFromAuthKey(key, dc)
		if err != nil {
			continue
		}
		if err := p.saveSessionData(meta.ID, data); err != nil {
			log.Printf("[funauth] authkey %s dc%d session_save: %v", meta.ID[:8], dc, err)
			continue
		}
		log.Printf("[funauth] authkey %s probe dc%d via %s", meta.ID[:8], dc, socksURLHost(socks))
		p.stopAccountClient(meta.ID)
		goSafe("funauth:connect:"+meta.ID, func() {
			p.connectAccount(meta, socks)
		})
		if p.waitAccountReady(meta.ID, 22*time.Second) {
			log.Printf("[funauth] authkey import ok %s dc%d", meta.Phone, dc)
			return
		}
		p.stopAccountClient(meta.ID)
		time.Sleep(300 * time.Millisecond)
	}
	log.Printf("[funauth] authkey %s: ни один DC не подошёл", meta.ID[:8])
	_ = p.remove(meta.ID)
}

// queueAuthKeyImport — принять ключ сразу, коннект/DC-probe в фоне.
func (p *funauthPool) queueAuthKeyImport(raw string, dcID int) (funauthAccountView, error) {
	if !p.configured() {
		return funauthAccountView{}, errFunauthNotConfigured
	}
	parsed, err := parseFunauthSessionInput(raw, dcID)
	if err != nil {
		return funauthAccountView{}, err
	}

	id := uuid.New().String()
	meta := funauthAccountMeta{
		ID:    id,
		Phone: "authkey:" + id[:8],
	}
	if err := p.saveMeta(meta); err != nil {
		return funauthAccountView{}, err
	}

	p.mu.Lock()
	p.accounts[id] = &funauthAccount{meta: meta, botMsg: make(chan string, 32)}
	p.mu.Unlock()

	goSafe("funauth:import:"+id, func() {
		funauthImportSem <- struct{}{}
		defer func() { <-funauthImportSem }()

		if parsed.Data != nil {
			if err := p.saveSessionData(id, parsed.Data); err != nil {
				log.Printf("[funauth] authkey session_save %s: %v", id[:8], err)
				_ = p.remove(id)
				return
			}
			socks := pickFarmLoginSOCKS()
			p.connectAccount(meta, socks)
			return
		}
		p.connectAuthKeyWithDCProbe(meta, parsed.Key, parsed.ForcedDC)
	})

	return funauthAccountView{
		ID:    id,
		Phone: meta.Phone,
		Ready: false,
	}, nil
}

func splitAuthKeyLines(raw string) []string {
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	return out
}

type funauthAuthKeyReject struct {
	Index   int    `json:"index"`
	Error   string `json:"error"`
	Preview string `json:"preview,omitempty"`
}

// queueAuthKeyImportBatch — много ключей (строки), без ожидания ready.
func (p *funauthPool) queueAuthKeyImportBatch(keys []string, dcID int) (accepted []funauthAccountView, rejected []funauthAuthKeyReject) {
	for i, raw := range keys {
		view, err := p.queueAuthKeyImport(raw, dcID)
		if err != nil {
			prev := strings.TrimSpace(raw)
			if len(prev) > 24 {
				prev = prev[:24] + "…"
			}
			rejected = append(rejected, funauthAuthKeyReject{Index: i, Error: err.Error(), Preview: prev})
			continue
		}
		accepted = append(accepted, view)
	}
	return accepted, rejected
}

// importAuthKey — устар. обёртка: очередь + короткое ожидание ready (для старых клиентов).
func (p *funauthPool) importAuthKey(raw string, dcID int) (funauthAccountView, error) {
	view, err := p.queueAuthKeyImport(raw, dcID)
	if err != nil {
		return funauthAccountView{}, err
	}
	if p.waitAccountReady(view.ID, 45*time.Second) {
		p.mu.Lock()
		acc := p.accounts[view.ID]
		if acc != nil {
			view = acc.view()
		}
		p.mu.Unlock()
		return view, nil
	}
	// Оставляем акк в пуле — коннект ещё может добить DC; не удаляем по таймауту UI.
	return view, nil
}

// activeTGLoginCount — сколько TG-логинов сейчас в процессе (для /health и binder).
func (p *funauthPool) activeTGLoginCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.pending)
}

// markFlood — аккаунт временно недоступен (Telegram FLOOD_WAIT). Ничего не стирает: ни привязки, ни сессию.
func (p *funauthPool) markFlood(id string, d time.Duration) {
	if p == nil || strings.TrimSpace(id) == "" || d <= 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if a := p.accounts[id]; a != nil {
		a.floodUntil = time.Now().Add(d)
	}
}

var funauthFloodWaitRe = regexp.MustCompile(`(?i)FLOOD_WAIT\s*\(?(\d+)`)

// funauthFloodWait: сколько секунд Telegram просит ждать (0 — это не FLOOD_WAIT).
func funauthFloodWait(errStr string) time.Duration {
	m := funauthFloodWaitRe.FindStringSubmatch(errStr)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 {
		return 0
	}
	return time.Duration(n) * time.Second
}
