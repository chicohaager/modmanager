// Package updates tells the ZimaOS user about module updates on the
// dashboard: a background check finds installed modules with a newer
// release (and a newer Module Manager), a card names them until they are
// installed. Without it an update was only visible inside the Module
// Manager's catalog (measured 2026-10-26: no background check, no card).
//
// Cards ("You should know", YSK) go through the ZimaOS message bus, as in
// Sync & Backup (zima-backup internal/dashboard) and ZFW. Measured on 1.8.0
// (KB §75): a card reaches only the dashboards open at that moment and is
// not stored, so it is re-sent every minute while updates wait; a footer
// button can only open a URL ("casaos-ui:open_files"); the body is cut
// after four lines (156 characters were visible at 1400 px).
package updates

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chicohaager/modmanager/internal/catalog"
)

// DefaultBusURLFile is where ZimaOS records the message bus address.
const DefaultBusURLFile = "/var/run/casaos/message-bus.url"

// CardID is stable, so a re-send replaces the card instead of stacking one.
const CardID = "modmanager:updates"

// budget is how many characters of the card body are shown (KB §75: four
// lines, 156 visible characters at 1400 px).
const budget = 140

const icon = "/modules/modmanager/appicon.svg"

// Source is the part of the catalog the check needs.
type Source interface {
	List(ctx context.Context) ([]catalog.Entry, []string, error)
	Self(ctx context.Context, installed string) catalog.SelfStatus
}

// Update is one module with a newer release. From is empty when the
// installed version is unknown (the catalog compared checksums).
type Update struct{ Title, From, To string }

// Find lists Module Manager itself (when a newer release exists) and every
// installed catalog module the catalog marks "update". A failed list is an
// error, never "no updates".
func Find(ctx context.Context, src Source, installed string) ([]Update, error) {
	entries, _, err := src.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []Update
	if s := src.Self(ctx, installed); s.Update {
		out = append(out, Update{Title: "Module Manager", From: s.Installed, To: s.Latest})
	}
	for _, e := range entries {
		if e.Installed && e.State == "update" {
			out = append(out, Update{Title: e.Title, From: e.InstalledVersion, To: e.Version})
		}
	}
	return out, nil
}

type words struct{ one, many, open string }

var texts = map[string]words{
	"en": {one: "1 module update: ", many: "{n} module updates: ", open: "Open Module Manager"},
	"de": {one: "1 Modul-Update: ", many: "{n} Modul-Updates: ", open: "Module Manager öffnen"},
}

func wordsFor(lang string) words {
	if w, ok := texts[lang]; ok {
		return w
	}
	return texts["en"]
}

// Text is the card body: the count and the modules, cut to the budget.
func Text(lang string, list []Update) string {
	w := wordsFor(lang)
	lead := w.one
	if len(list) != 1 {
		lead = strings.ReplaceAll(w.many, "{n}", fmt.Sprint(len(list)))
	}
	items := make([]string, len(list))
	for i, u := range list {
		if u.From != "" {
			items[i] = u.Title + " " + u.From + " → " + u.To
		} else {
			items[i] = u.Title + " → " + u.To
		}
	}
	s := []rune(lead + strings.Join(items, ", "))
	if len(s) > budget {
		s = append(s[:budget-1], '…')
	}
	return string(s)
}

type card struct {
	ID         string  `json:"id"`
	CardType   string  `json:"cardType"`
	RenderType string  `json:"renderType"`
	Content    content `json:"content"`
}

type content struct {
	TitleIcon        string     `json:"titleIcon"`
	TitleText        string     `json:"titleText"`
	BodyIconWithText *iconText  `json:"bodyIconWithText,omitempty"`
	FooterActions    []footerAc `json:"footerActions,omitempty"`
}

type iconText struct {
	Icon        string `json:"icon"`
	Description string `json:"description"`
}

type footerAc struct {
	Side       string `json:"side"`
	Style      string `json:"style"`
	Text       string `json:"text"`
	MessageBus busAct `json:"messageBus"`
}

type busAct struct {
	Key     string `json:"key"`
	Payload string `json:"payload"`
}

func build(lang string, list []Update) card {
	open, _ := json.Marshal(map[string]string{"url": "modules/modmanager/index.html#catalog"})
	return card{
		ID: CardID, CardType: "long-notice", RenderType: "icon-text-notice",
		Content: content{
			TitleIcon: icon, TitleText: "Module Manager",
			BodyIconWithText: &iconText{Icon: icon, Description: Text(lang, list)},
			FooterActions: []footerAc{{Side: "right", Style: "primary", Text: wordsFor(lang).open,
				MessageBus: busAct{Key: "casaos-ui:open_files", Payload: string(open)}}},
		},
	}
}

// Notifier posts and withdraws the card. The zero BusURLFile disables it.
type Notifier struct {
	BusURLFile string
	Client     *http.Client
}

func (n Notifier) busURL() (string, error) {
	b, err := os.ReadFile(n.BusURLFile)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(strings.TrimSpace(string(b)))
	if err != nil || u.Scheme != "http" || u.Hostname() != "127.0.0.1" {
		// the bus is a local service; never follow the file anywhere else
		return "", fmt.Errorf("message bus url %q is not a loopback http url", strings.TrimSpace(string(b)))
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func (n Notifier) publish(ctx context.Context, event string, props map[string]string) error {
	if n.BusURLFile == "" {
		return nil
	}
	base, err := n.busURL()
	if err != nil {
		return err
	}
	body, _ := json.Marshal(props)
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v2/message_bus/event/ysk/"+url.PathEscape(event), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	c := n.Client
	if c == nil {
		c = http.DefaultClient
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("message bus %s: HTTP %d", event, resp.StatusCode)
	}
	return nil
}

// Show sends (or re-sends) the card.
func (n Notifier) Show(ctx context.Context, lang string, list []Update) error {
	b, err := json.Marshal(build(lang, list))
	if err != nil {
		return err
	}
	return n.publish(ctx, "ysk:card:upsert", map[string]string{"card:body": string(b)})
}

// Withdraw removes the card from the dashboards that are open right now.
func (n Notifier) Withdraw(ctx context.Context) error {
	return n.publish(ctx, "ysk:card:delete", map[string]string{"card:id": CardID})
}

// Keeper holds the result of the last check and re-sends the card every
// round while it names updates; it withdraws the card once when the list
// becomes empty. Before the first check it sends nothing.
type Keeper struct {
	Notifier Notifier
	Lang     func() string

	mu      sync.Mutex
	list    []Update
	checked bool
	shown   bool
	sent    string
	lastErr string
	kick    chan struct{}
}

// Kick asks Run for a check now: after an install from the catalog the card
// must not wait for the next 12-hour check. It never blocks.
func (k *Keeper) Kick() {
	k.mu.Lock()
	if k.kick == nil {
		k.kick = make(chan struct{}, 1)
	}
	c := k.kick
	k.mu.Unlock()
	select {
	case c <- struct{}{}:
	default: // a check is asked for already
	}
}

func (k *Keeper) kicks() <-chan struct{} {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.kick == nil {
		k.kick = make(chan struct{}, 1)
	}
	return k.kick
}

// Set stores the result of a check.
func (k *Keeper) Set(list []Update) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.checked {
		// a card of the previous run may still sit in an open dashboard
		k.checked, k.shown = true, true
	}
	k.list = append([]Update(nil), list...)
}

// Round is one look: show, withdraw or nothing.
func (k *Keeper) Round(ctx context.Context) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if !k.checked {
		return
	}
	var err error
	switch {
	case len(k.list) > 0:
		lang := k.Lang()
		if err = k.Notifier.Show(ctx, lang, k.list); err == nil {
			k.shown = true
			if s := Text("en", k.list); s != k.sent {
				log.Printf("[modmanager] dashboard card: %s", s)
				k.sent = s
			}
		}
	case k.shown:
		if err = k.Notifier.Withdraw(ctx); err == nil {
			k.shown = false
			k.sent = ""
			// also after a restart, when the card may stem from the previous run
			log.Printf("[modmanager] dashboard card withdrawn: no module updates")
		}
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	if msg != k.lastErr {
		if msg != "" {
			log.Printf("[modmanager] dashboard card: %s", msg)
		}
		k.lastErr = msg
	}
}

// Run checks for updates (the first time after delay, at once on Kick) and
// runs a card round every minute, until ctx ends. The next check comes after
// every, or after waiting while the card names updates: an update installed
// past the catalog (a module's own installer, zpkg by hand) sends no Kick.
func Run(ctx context.Context, k *Keeper, src Source, installed string, delay, every, waiting time.Duration) {
	check := time.NewTimer(delay)
	defer check.Stop()
	round := time.NewTicker(time.Minute)
	defer round.Stop()
	kick := k.kicks()
	look := func() {
		cctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
		list, err := Find(cctx, src, installed)
		cancel()
		next := every
		if err != nil {
			log.Printf("[modmanager] update check: %v", err)
		} else {
			k.Set(list)
			if len(list) > 0 {
				next = waiting
			}
		}
		k.Round(ctx)
		if !check.Stop() {
			select {
			case <-check.C:
			default:
			}
		}
		check.Reset(next)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-check.C:
			look()
		case <-kick:
			look()
		case <-round.C:
			k.Round(ctx)
		}
	}
}

// The UI's language, reported by the page (it knows the ZimaOS user's
// setting, the backend has no token for it). Only "en" and "de" exist.
func langFile(dir string) string { return filepath.Join(dir, "ui-lang") }

// ReadLang returns the stored language, "en" when there is none.
func ReadLang(dir string) string {
	b, err := os.ReadFile(langFile(dir))
	if err == nil && strings.TrimSpace(string(b)) == "de" {
		return "de"
	}
	return "en"
}

// WriteLang stores "en" or "de".
func WriteLang(dir, lang string) error {
	if lang != "en" && lang != "de" {
		return fmt.Errorf("language %q: en or de", lang)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(langFile(dir), []byte(lang+"\n"), 0o644)
}
