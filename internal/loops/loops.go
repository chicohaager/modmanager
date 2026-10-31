// Package loops counts the loop devices old system-extension states hold and puts a
// card on the dashboard when they pile up.
//
// Every systemd-sysext refresh (each module install, update, removal, hiding a
// built-in app) mounts all extension images again, each on a new loop device, and
// lays a new overlay over /usr. The old overlay lives on — with the loop devices of
// all its images — as long as a process started under it still runs or a service
// namespace still has it mounted (measured on ZimaOS 1.8.0, systemd 258.7, kernel
// 6.18.9, KB §79.15). Nothing frees them but restarting what holds them, in practice
// a reboot of ZimaOS. Measured on the second test NAS: 634 loop devices after a day of tests, 57
// right after a reboot. Module Manager does not restart anything for it; it says so.
package loops

import (
	"context"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/chicohaager/lintux-modkit/ysk"
)

// DefaultSysBlock is where the kernel lists block devices.
const DefaultSysBlock = "/sys/block"

// DefaultProc is the host's /proc.
const DefaultProc = "/proc"

// Threshold of old states from which the card shows. Three states (two old) are
// there after every boot of 1.8.0 (measured: 3 loop devices per image 150 s after
// boot); each refresh adds one while anything from the state before still runs.
const Threshold = 10

// CardID is stable, so a re-send replaces the card instead of stacking one.
const CardID = "modmanager:loops"

const icon = "/modules/modmanager/appicon.svg"

// Stats is what GET /modmanager/loops returns.
type Stats struct {
	Loops     int `json:"loops"`      // loop devices bound to a system-extension image
	Images    int `json:"images"`     // different image files among them
	OldStates int `json:"old_states"` // the most loop devices one image has, minus the current one
	Threshold int `json:"threshold"`
	// StaleUsr names the services whose mount namespace still shows the /usr of their
	// start on top of the current one (measured: systemd-logind, systemd-networkd,
	// virtlogd after a refresh); they keep using the old files until they restart.
	StaleUsr []string `json:"stale_usr"`
}

// StaleUsr lists, per mount namespace, the unit of a process whose topmost /usr mount
// is not the host's (/proc/1/mountinfo). Sorted, without duplicates.
func StaleUsr(proc string) []string {
	host := topUsr(filepath.Join(proc, "1", "mountinfo"))
	if host == "" {
		return []string{}
	}
	pids, _ := filepath.Glob(filepath.Join(proc, "[0-9]*"))
	seen := map[string]bool{}
	units := map[string]bool{}
	for _, p := range pids {
		ns, err := os.Readlink(filepath.Join(p, "ns", "mnt"))
		if err != nil || seen[ns] {
			continue
		}
		seen[ns] = true
		if top := topUsr(filepath.Join(p, "mountinfo")); top != "" && top != host {
			units[unitOf(filepath.Join(p, "cgroup"))] = true
		}
	}
	out := []string{}
	for u := range units {
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}

// topUsr is the device (major:minor) of the /usr mount a path lookup reaches: the one
// no other /usr mount sits on. mountinfo lists stacked mounts in no fixed order
// (measured: the old /usr of systemd-logind, on top, came first).
func topUsr(mountinfo string) string {
	b, err := os.ReadFile(mountinfo)
	if err != nil {
		return ""
	}
	type m struct{ id, parent, dev string }
	var usr []m
	for _, l := range strings.Split(string(b), "\n") {
		f := strings.Fields(l)
		if len(f) > 4 && f[4] == "/usr" {
			usr = append(usr, m{f[0], f[1], f[2]})
		}
	}
	under := map[string]bool{}
	for _, u := range usr {
		under[u.parent] = true
	}
	for _, u := range usr {
		if !under[u.id] {
			return u.dev
		}
	}
	return ""
}

// unitOf takes the unit from the process' cgroup ("0::/system.slice/systemd-logind.service").
func unitOf(cgroup string) string {
	b, err := os.ReadFile(cgroup)
	if err != nil {
		return "?"
	}
	line := strings.TrimSpace(strings.Split(string(b), "\n")[0])
	return path.Base(line)
}

// Measure reads the loop devices from sys (/sys/block). An image counts by the file
// name its loop device names (" (deleted)" cut: a replaced image of the same module
// still belongs to an old state).
func Measure(sys string) (Stats, error) {
	st := Stats{Threshold: Threshold}
	dirs, err := filepath.Glob(filepath.Join(sys, "loop[0-9]*"))
	if err != nil {
		return st, err
	}
	per := map[string]int{}
	for _, d := range dirs {
		b, err := os.ReadFile(filepath.Join(d, "loop", "backing_file"))
		if err != nil {
			continue // not bound
		}
		f := strings.TrimSuffix(strings.TrimSpace(string(b)), " (deleted)")
		base := filepath.Base(f)
		if !strings.Contains(base, ".raw") {
			continue // not an extension image
		}
		per[strings.TrimSuffix(strings.TrimSuffix(base, ".bak"), ".raw")]++
		st.Loops++
	}
	st.Images = len(per)
	for _, n := range per {
		st.OldStates = max(st.OldStates, n-1)
	}
	return st, nil
}

type words struct{ text, open string }

var texts = map[string]words{
	"en": {text: "{old} old system states hold {loops} loop devices; module installs and updates can add more. A restart of ZimaOS frees them.", open: "Open Module Manager"},
	"de": {text: "{old} alte Systemstände belegen {loops} Loop-Geräte; Installationen und Updates von Modulen können weitere hinzufügen. Ein Neustart von ZimaOS gibt sie frei.", open: "Module Manager öffnen"},
	"fr": {text: "{old} anciens états système occupent {loops} périphériques loop ; les installations et mises à jour de modules peuvent en ajouter. Un redémarrage de ZimaOS les libère.", open: "Ouvrir Module Manager"},
}

// Text is the card body.
func Text(lang string, st Stats) string {
	w, ok := texts[lang]
	if !ok {
		w = texts["en"]
	}
	return strings.NewReplacer("{old}", fmt.Sprint(st.OldStates), "{loops}", fmt.Sprint(st.Loops)).Replace(w.text)
}

func open(lang string) string {
	if w, ok := texts[lang]; ok {
		return w.open
	}
	return texts["en"].open
}

// Keeper measures every round and shows the card while OldStates reaches the
// threshold; it withdraws it once when the number falls below (after a reboot).
type Keeper struct {
	Notifier ysk.Notifier
	Lang     func() string
	Sys      string // DefaultSysBlock; tests point it into a temp dir

	mu    sync.Mutex
	shown bool
	sent  string
	last  Stats
}

// Last is the last measurement (for the API).
func (k *Keeper) Last() Stats {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.last
}

// Round measures and shows, withdraws or does nothing.
func (k *Keeper) Round(ctx context.Context) {
	st, err := Measure(k.Sys)
	k.mu.Lock()
	defer k.mu.Unlock()
	if err != nil {
		log.Printf("[modmanager] loop devices: %v", err)
		return
	}
	k.last = st
	switch {
	case st.OldStates >= Threshold:
		lang := k.Lang()
		if err := k.Notifier.Show(ctx, ysk.NewCard(CardID, "Module Manager", icon, Text(lang, st), open(lang), "modules/modmanager/index.html#installed")); err != nil {
			log.Printf("[modmanager] loop card: %v", err)
			return
		}
		k.shown = true
		if s := Text("en", st); s != k.sent {
			log.Printf("[modmanager] loop card: %s", s)
			k.sent = s
		}
	case k.shown:
		if err := k.Notifier.Withdraw(ctx, CardID); err != nil {
			log.Printf("[modmanager] loop card: %v", err)
			return
		}
		k.shown, k.sent = false, ""
		log.Printf("[modmanager] loop card withdrawn: %d old system states", st.OldStates)
	}
}

// Run does a round now and every minute (ZimaOS does not store cards, KB §75) until
// ctx ends.
func Run(ctx context.Context, k *Keeper) {
	k.mu.Lock()
	k.shown = true // a card of the previous run may still sit in an open dashboard
	k.mu.Unlock()
	k.Round(ctx)
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			k.Round(ctx)
		}
	}
}
