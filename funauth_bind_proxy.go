package main

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	funauthBindProxiesFile = "bind_proxies.json"
	// Типичный Telegram DC2 — хватает, чтобы отсечь «только Minecraft» SOCKS вроде Lunar.
	funauthProbeHost = "149.154.167.50"
	funauthProbePort = "443"
	funauthProbeTTL  = 90 * time.Second
)

// Старые nitron-прокси 502 (до Lunar) + остальные рабочие слоты фермы на момент смены.
// Боты остаются на Lunar в ip.json — этот список только для FunAuth MTProto.
var funauthBindProxySeed = []string{
	"socks5://privetnitron:jacb9eSh7e@50.114.26.77:50101",
	"socks5://privetnitron:jacb9eSh7e@212.236.230.70:50101",
	"socks5://privetnitron:jacb9eSh7e@85.120.129.58:50101",
	"socks5://privetnitron:jacb9eSh7e@85.120.131.231:50101",
	"socks5://privetnitron:jacb9eSh7e@50.114.26.224:50101",
	"socks5://privetnitron:jacb9eSh7e@85.120.131.214:50101",
	"socks5://privetnitron:jacb9eSh7e@85.120.130.180:50101",
	"socks5://privetnitron:jacb9eSh7e@85.120.130.201:50101",
	"socks5://privetnitron:jacb9eSh7e@212.236.230.10:50101",
	"socks5://privetnitron:jacb9eSh7e@85.120.129.90:50101",
	"socks5://privetnitron:jacb9eSh7e@85.120.128.95:50101",
	"socks5://privetnitron:jacb9eSh7e@85.120.130.31:50101",
	"socks5://privetnitron:jacb9eSh7e@85.120.131.133:50101",
}

type funauthBindProxy struct {
	ID        string `json:"id"`
	URL       string `json:"url"`
	Label     string `json:"label,omitempty"`
	CreatedAt int64  `json:"createdAt,omitempty"`
}

type funauthBindProxyView struct {
	ID     string `json:"id"`
	URL    string `json:"url"`
	Host   string `json:"host"`
	Label  string `json:"label,omitempty"`
	OK     *bool  `json:"ok"` // null = ещё не проверяли
	Err    string `json:"err,omitempty"`
	CheckedAt int64 `json:"checkedAt,omitempty"`
}

type bindProxyCacheEntry struct {
	ok        bool
	err       string
	checkedAt time.Time
}

type funauthBindProxyPool struct {
	mu      sync.Mutex
	path    string
	items   []funauthBindProxy
	cache   map[string]bindProxyCacheEntry
	rr      uint32
}

var funauthBindProxies = &funauthBindProxyPool{}

func (p *funauthBindProxyPool) init(sessionsDir string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.path = filepath.Join(sessionsDir, funauthBindProxiesFile)
	p.cache = make(map[string]bindProxyCacheEntry)
	if err := p.loadLocked(); err != nil {
		log.Printf("[funauth] bind-proxies load: %v", err)
	}
	if len(p.items) == 0 {
		for _, u := range funauthBindProxySeed {
			p.items = append(p.items, funauthBindProxy{
				ID:        bindProxyID(u),
				URL:       u,
				Label:     "seed",
				CreatedAt: time.Now().UnixMilli(),
			})
		}
		if err := p.saveLocked(); err != nil {
			log.Printf("[funauth] bind-proxies seed save: %v", err)
		} else {
			log.Printf("[funauth] bind-proxies: seeded %d nitron SOCKS", len(p.items))
		}
	} else {
		log.Printf("[funauth] bind-proxies: %d URL(s)", len(p.items))
	}
}

func bindProxyID(rawURL string) string {
	sum := sha1.Sum([]byte(strings.TrimSpace(rawURL)))
	return hex.EncodeToString(sum[:8])
}

func (p *funauthBindProxyPool) loadLocked() error {
	raw, err := os.ReadFile(p.path)
	if err != nil {
		if os.IsNotExist(err) {
			p.items = nil
			return nil
		}
		return err
	}
	var items []funauthBindProxy
	if err := json.Unmarshal(raw, &items); err != nil {
		return err
	}
	p.items = items
	return nil
}

func (p *funauthBindProxyPool) saveLocked() error {
	if p.path == "" {
		return fmt.Errorf("bind-proxies path empty")
	}
	raw, err := json.MarshalIndent(p.items, "", "  ")
	if err != nil {
		return err
	}
	tmp := p.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.path)
}

func normalizeBindProxyURL(raw string) (string, error) {
	u := strings.TrimSpace(raw)
	if u == "" {
		return "", fmt.Errorf("empty_url")
	}
	if !strings.Contains(u, "://") {
		u = "socks5://" + u
	}
	parsed, err := url.Parse(u)
	if err != nil {
		return "", err
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "socks5" && scheme != "socks5h" {
		return "", fmt.Errorf("need socks5://")
	}
	if parsed.Hostname() == "" || parsed.Port() == "" {
		return "", fmt.Errorf("host:port required")
	}
	// всегда socks5://user:pass@host:port
	out := &url.URL{
		Scheme: "socks5",
		Host:   net.JoinHostPort(parsed.Hostname(), parsed.Port()),
		User:   parsed.User,
	}
	return out.String(), nil
}

func (p *funauthBindProxyPool) list() []funauthBindProxyView {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]funauthBindProxyView, 0, len(p.items))
	for _, it := range p.items {
		v := funauthBindProxyView{
			ID:    it.ID,
			URL:   it.URL,
			Host:  socksURLHost(it.URL),
			Label: it.Label,
		}
		if c, ok := p.cache[it.URL]; ok {
			okCopy := c.ok
			v.OK = &okCopy
			v.Err = c.err
			v.CheckedAt = c.checkedAt.UnixMilli()
		}
		out = append(out, v)
	}
	return out
}

func (p *funauthBindProxyPool) add(rawURL, label string) (funauthBindProxyView, error) {
	norm, err := normalizeBindProxyURL(rawURL)
	if err != nil {
		return funauthBindProxyView{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	id := bindProxyID(norm)
	for _, it := range p.items {
		if it.ID == id || it.URL == norm {
			return funauthBindProxyView{}, fmt.Errorf("already_exists")
		}
	}
	item := funauthBindProxy{
		ID:        id,
		URL:       norm,
		Label:     strings.TrimSpace(label),
		CreatedAt: time.Now().UnixMilli(),
	}
	p.items = append(p.items, item)
	if err := p.saveLocked(); err != nil {
		p.items = p.items[:len(p.items)-1]
		return funauthBindProxyView{}, err
	}
	delete(p.cache, norm)
	return funauthBindProxyView{ID: item.ID, URL: item.URL, Host: socksURLHost(item.URL), Label: item.Label}, nil
}

func (p *funauthBindProxyPool) remove(id string) bool {
	id = strings.TrimSpace(id)
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, it := range p.items {
		if it.ID != id {
			continue
		}
		url := it.URL
		p.items = append(p.items[:i], p.items[i+1:]...)
		delete(p.cache, url)
		_ = p.saveLocked()
		return true
	}
	return false
}

func probeSOCKSTelegram(proxyURL string) (bool, string) {
	dialer, err := socks5ContextDialer(proxyURL)
	if err != nil {
		return false, err.Error()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(funauthProbeHost, funauthProbePort))
	if err != nil {
		return false, err.Error()
	}
	_ = conn.Close()
	return true, ""
}

func (p *funauthBindProxyPool) cachedOK(proxyURL string) (ok bool, known bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	c, hit := p.cache[proxyURL]
	if !hit {
		return false, false
	}
	if time.Since(c.checkedAt) > funauthProbeTTL {
		return false, false
	}
	return c.ok, true
}

func (p *funauthBindProxyPool) remember(proxyURL string, ok bool, errMsg string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.cache[proxyURL] = bindProxyCacheEntry{ok: ok, err: errMsg, checkedAt: time.Now()}
}

func (p *funauthBindProxyPool) checkAll() []funauthBindProxyView {
	p.mu.Lock()
	urls := make([]string, 0, len(p.items))
	for _, it := range p.items {
		urls = append(urls, it.URL)
	}
	p.mu.Unlock()
	for _, u := range urls {
		ok, errMsg := probeSOCKSTelegram(u)
		p.remember(u, ok, errMsg)
		host := socksURLHost(u)
		if ok {
			log.Printf("[funauth] bind-proxy OK %s", host)
		} else {
			log.Printf("[funauth] bind-proxy FAIL %s: %s", host, errMsg)
		}
	}
	return p.list()
}

// pickBindSOCKS — живой SOCKS из пула (Telegram DC проходит). Пусто → xray/без прокси.
func pickBindSOCKS() string {
	p := funauthBindProxies
	p.mu.Lock()
	n := len(p.items)
	if n == 0 {
		p.mu.Unlock()
		return ""
	}
	start := int(atomic.AddUint32(&p.rr, 1)-1) % n
	order := make([]funauthBindProxy, 0, n)
	for i := 0; i < n; i++ {
		order = append(order, p.items[(start+i)%n])
	}
	p.mu.Unlock()

	for _, it := range order {
		if ok, known := p.cachedOK(it.URL); known {
			if ok {
				return it.URL
			}
			continue
		}
		ok, errMsg := probeSOCKSTelegram(it.URL)
		p.remember(it.URL, ok, errMsg)
		if ok {
			log.Printf("[funauth] bind-proxy pick %s", socksURLHost(it.URL))
			return it.URL
		}
		log.Printf("[funauth] bind-proxy skip %s: %s", socksURLHost(it.URL), errMsg)
	}
	log.Printf("[funauth] bind-proxy: нет рабочих SOCKS в пуле")
	return ""
}

// pickFarmLoginSOCKS — для FunAuth login/connect: пул bind-прокси (не ip.json ботов).
func pickFarmLoginSOCKS() string {
	return pickBindSOCKS()
}
