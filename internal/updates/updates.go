// Package updates tells the ZimaOS user about module updates on the
// dashboard: a background check finds installed modules with a newer
// release (and a newer Module Manager), a card names them until they are
// installed. Without it an update was only visible inside the Module
// Manager's catalog (measured 2026-10-26: no background check, no card).
//
// The card goes through the ZimaOS message bus (lintux-modkit ysk, shared
// with Sync & Backup). Measured on 1.8.0 (KB §75): a card reaches only the
// dashboards open at that moment and is not stored, so it is re-sent every
// minute while updates wait; a footer button can only open a URL; the body
// is cut after four lines (156 characters were visible at 1400 px).
package updates

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chicohaager/lintux-modkit/ysk"
	"github.com/chicohaager/modmanager/internal/catalog"
)

// DefaultBusURLFile is where ZimaOS records the message bus address.
const DefaultBusURLFile = ysk.DefaultBusURLFile

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

// ErrIncomplete: GitHub did not answer for some entries (rate limit, offline), so
// the list may miss updates; it is no reason to withdraw the card.
var ErrIncomplete = errors.New("update check incomplete: GitHub did not answer for every module")

// IncompleteError names the entries GitHub did not answer for; errors.Is(err, ErrIncomplete).
type IncompleteError struct{ Failed []string }

func (e *IncompleteError) Error() string {
	return ErrIncomplete.Error() + " (" + strings.Join(e.Failed, ", ") + ")"
}

func (e *IncompleteError) Is(target error) bool { return target == ErrIncomplete }

// transient are the catalog's error codes of a question GitHub did not answer;
// the others (no release, no image …) are answers.
var transient = map[string]bool{"rate_limit": true, "unreachable": true}

// Update is one module with a newer release. From is empty when the
// installed version is unknown (the catalog compared checksums).
type Update struct{ Title, From, To string }

// Find lists Module Manager itself (when a newer release exists) and every
// installed catalog module the catalog marks "update". A failed list is an
// error, never "no updates"; entries GitHub did not answer for come back as an
// *IncompleteError next to the updates that were found.
func Find(ctx context.Context, src Source, installed string) ([]Update, error) {
	entries, _, err := src.List(ctx)
	if err != nil {
		return nil, err
	}
	var out []Update
	var failed []string
	if s := src.Self(ctx, installed); s.Update {
		out = append(out, Update{Title: "Module Manager", From: s.Installed, To: s.Latest})
	} else if s.Error != "" {
		failed = append(failed, "Module Manager")
	}
	for _, e := range entries {
		if transient[e.ErrorCode] {
			failed = append(failed, e.Title)
			continue
		}
		if e.Installed && e.State == "update" {
			out = append(out, Update{Title: e.Title, From: e.InstalledVersion, To: e.Version})
		}
	}
	if len(failed) > 0 {
		return out, &IncompleteError{Failed: failed}
	}
	return out, nil
}

type words struct{ one, many, open string }

var texts = map[string]words{
	"en": {one: "1 module update: ", many: "{n} module updates: ", open: "Open Module Manager"},
	"de": {one: "1 Modul-Update: ", many: "{n} Modul-Updates: ", open: "Module Manager öffnen"},
	"fr": {one: "1 mise à jour de module : ", many: "{n} mises à jour de modules : ", open: "Ouvrir Module Manager"},
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

// card is the YSK card (lintux-modkit ysk).
type card = ysk.Card

func build(lang string, list []Update) card {
	return ysk.NewCard(CardID, "Module Manager", icon, Text(lang, list), wordsFor(lang).open, "modules/modmanager/index.html#catalog")
}

// Notifier posts and withdraws the card. The zero BusURLFile disables it.
type Notifier struct {
	BusURLFile string
	Client     *http.Client
}

func (n Notifier) bus() ysk.Notifier { return ysk.Notifier{BusURLFile: n.BusURLFile, Client: n.Client} }

// Show sends (or re-sends) the card.
func (n Notifier) Show(ctx context.Context, lang string, list []Update) error {
	return n.bus().Show(ctx, build(lang, list))
}

// Withdraw removes the card from the dashboards that are open right now.
func (n Notifier) Withdraw(ctx context.Context) error {
	return n.bus().Withdraw(ctx, CardID)
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
	once    sync.Once // makes kick; Kick must not take mu, which a card round holds while it talks to the bus
}

// Kick asks Run for a check now: after an install from the catalog the card
// must not wait for the next 12-hour check. It never blocks.
func (k *Keeper) Kick() {
	select {
	case k.kicks() <- struct{}{}:
	default: // a check is asked for already
	}
}

func (k *Keeper) kicks() chan struct{} {
	k.once.Do(func() { k.kick = make(chan struct{}, 1) })
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

// Keep is Set for an incomplete check: the modules GitHub did not answer for
// keep what the card said about them before.
func (k *Keeper) Keep(list []Update, failed []string) {
	k.mu.Lock()
	prev := append([]Update(nil), k.list...)
	first := !k.checked
	k.mu.Unlock()
	if first && len(list) == 0 {
		return // nothing known yet: no reason to withdraw a card of the previous run
	}
	miss := map[string]bool{}
	for _, t := range failed {
		miss[t] = true
	}
	for _, u := range prev {
		if miss[u.Title] {
			list = append(list, u)
		}
	}
	k.Set(list)
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
		var inc *IncompleteError
		switch {
		case err == nil:
			k.Set(list)
			if len(list) > 0 {
				next = waiting
			}
		case errors.As(err, &inc):
			// GitHub did not answer for some: keep what is known, ask again soon
			log.Printf("[modmanager] update check: %v", err)
			k.Keep(list, inc.Failed)
			next = waiting
		default:
			log.Printf("[modmanager] update check: %v", err)
			next = waiting
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
// setting, the backend has no token for it). Only the languages of texts exist.
func langFile(dir string) string { return filepath.Join(dir, "ui-lang") }

// ReadLang returns the stored language, "en" when there is none.
func ReadLang(dir string) string {
	b, err := os.ReadFile(langFile(dir))
	lang := strings.TrimSpace(string(b))
	if _, ok := texts[lang]; err == nil && ok {
		return lang
	}
	return "en"
}

// WriteLang stores "en", "de" or "fr".
func WriteLang(dir, lang string) error {
	if _, ok := texts[lang]; !ok {
		return fmt.Errorf("language %q: en, de or fr", lang)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(langFile(dir), []byte(lang+"\n"), 0o644)
}
