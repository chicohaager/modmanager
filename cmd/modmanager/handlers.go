package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/chicohaager/lintux-modkit/httpx"
	"github.com/chicohaager/modmanager/internal/catalog"
	"github.com/chicohaager/modmanager/internal/loops"
	"github.com/chicohaager/modmanager/internal/progress"
	"github.com/chicohaager/modmanager/internal/state"
	"github.com/chicohaager/modmanager/internal/sysapps"
	"github.com/chicohaager/modmanager/internal/updates"
)

// catalogTimeout covers downloading an image and zpkg installing it.
const catalogTimeout = 10 * time.Minute

// actionMu serialises every changing action: hiding an app and zpkg
// installing a module both run systemd-sysext refresh over all of /usr.
var actionMu sync.Mutex

// jobs is the log of the running (or last) install, uninstall or self-update; the
// catalog writes into it (main sets catalog.Progress) and the page polls it.
var jobs = &progress.Log{}

// catalogService is what the handlers need from catalog.Catalog.
type catalogService interface {
	List(ctx context.Context) ([]catalog.Entry, []string, error)
	Install(ctx context.Context, id string) error
	Uninstall(ctx context.Context, name string) error
	AddRepo(ctx context.Context, repo string) (string, error)
	RemoveRepo(repo string) error
	Self(ctx context.Context, installed string) catalog.SelfStatus
	SelfUpdate(ctx context.Context, installed string) (string, error)
}

// appService is what the handlers need from sysapps.Manager; tests fake it.
type appService interface {
	List(ctx context.Context) ([]sysapps.App, error)
	Hide(ctx context.Context, id string) error
	Restore(ctx context.Context, id string) error
	Purge(ctx context.Context, id, confirm string) error
}

func newMux(svc appService, cat catalogService, verify func(http.Handler) http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	logged := httpx.Logging("modmanager", maxRequestBody)
	mux.Handle(routePrefix+"/health", logged(http.HandlerFunc(healthHandler)))
	mux.Handle(routePrefix+"/apps", logged(verify(appsHandler(svc))))
	mux.Handle(routePrefix+"/apps/hide", logged(verify(actionHandler(svc, "hide"))))
	mux.Handle(routePrefix+"/apps/restore", logged(verify(actionHandler(svc, "restore"))))
	mux.Handle(routePrefix+"/apps/purge", logged(verify(actionHandler(svc, "purge"))))
	mux.Handle(routePrefix+"/catalog", logged(verify(catalogListHandler(cat))))
	mux.Handle(routePrefix+"/catalog/install", logged(verify(catalogActionHandler(cat, "install"))))
	mux.Handle(routePrefix+"/catalog/uninstall", logged(verify(catalogActionHandler(cat, "uninstall"))))
	mux.Handle(routePrefix+"/catalog/repos", logged(verify(catalogActionHandler(cat, "add-repo"))))
	mux.Handle(routePrefix+"/catalog/repos/remove", logged(verify(catalogActionHandler(cat, "remove-repo"))))
	mux.Handle(routePrefix+"/self", logged(verify(selfHandler(cat))))
	mux.Handle(routePrefix+"/self/update", logged(verify(selfUpdateHandler(cat))))
	mux.Handle(routePrefix+"/ui-lang", logged(verify(uiLangHandler())))
	// Not logged: the page asks about twice a second while an action runs.
	mux.Handle(routePrefix+"/progress", verify(progressHandler()))
	mux.Handle(routePrefix+"/loops", logged(verify(loopsHandler())))
	return mux
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpx.MethodNotAllowed(w)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "version": version})
}

func appsHandler(svc appService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httpx.MethodNotAllowed(w)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), actionTimeout)
		defer cancel()
		apps, err := svc.List(ctx)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"apps": apps})
	}
}

type actionRequest struct {
	ID      string `json:"id"`
	Confirm string `json:"confirm"`
}

func actionHandler(svc appService, action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			httpx.MethodNotAllowed(w)
			return
		}
		var req actionRequest
		if !httpx.Decode(w, r, &req) {
			return
		}
		if req.ID == "" {
			httpx.WriteError(w, http.StatusBadRequest, "missing_id", "id is required")
			return
		}
		// The action outlives a closed browser tab: a refresh cut in half
		// would leave /usr unmerged.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), actionTimeout)
		defer cancel()
		actionMu.Lock()
		defer actionMu.Unlock()
		// sysapps reports through the context: Reconcile at start runs the same code without a job.
		jobs.Start(action, req.ID)
		ctx = progress.NewContext(ctx, jobs)
		var err error
		switch action {
		case "hide":
			err = svc.Hide(ctx, req.ID)
		case "restore":
			err = svc.Restore(ctx, req.ID)
		case "purge":
			err = svc.Purge(ctx, req.ID, req.Confirm)
		}
		jobs.Finish(err)
		if err != nil {
			log.Printf("[modmanager] %s %s: %v", action, req.ID, err)
			writeServiceError(w, err)
			return
		}
		log.Printf("[modmanager] %s %s: done", action, req.ID)
		apps, err := svc.List(ctx)
		if err != nil {
			writeServiceError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"apps": apps})
	}
}

func writeServiceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sysapps.ErrUnknown):
		httpx.WriteError(w, http.StatusNotFound, "unknown_app", err.Error())
	case errors.Is(err, sysapps.ErrBlocked):
		httpx.WriteError(w, http.StatusConflict, "blocked", err.Error())
	case errors.Is(err, sysapps.ErrNotHid):
		httpx.WriteError(w, http.StatusConflict, "not_hidden", err.Error())
	case errors.Is(err, sysapps.ErrMedia):
		httpx.WriteError(w, http.StatusConflict, "media_found", err.Error())
	case errors.Is(err, sysapps.ErrConfirm):
		httpx.WriteError(w, http.StatusBadRequest, "confirm_mismatch", err.Error())
	default:
		httpx.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
	}
}

func catalogListHandler(cat catalogService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httpx.MethodNotAllowed(w)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		entries, repos, err := cat.List(ctx)
		if err != nil {
			writeCatalogError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"entries": entries, "repos": repos})
	}
}

type catalogRequest struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Repo string `json:"repo"`
}

func catalogActionHandler(cat catalogService, action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			httpx.MethodNotAllowed(w)
			return
		}
		var req catalogRequest
		if !httpx.Decode(w, r, &req) {
			return
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), catalogTimeout)
		defer cancel()
		actionMu.Lock()
		var err error
		target := req.ID + req.Name + req.Repo
		switch action {
		case "install":
			jobs.Start(action, req.ID)
			err = cat.Install(ctx, req.ID)
			jobs.Finish(err)
		case "uninstall":
			jobs.Start(action, req.Name)
			err = cat.Uninstall(ctx, req.Name)
			jobs.Finish(err)
		case "add-repo":
			_, err = cat.AddRepo(ctx, req.Repo)
		case "remove-repo":
			err = cat.RemoveRepo(req.Repo)
		}
		actionMu.Unlock()
		if err != nil {
			log.Printf("[modmanager] catalog %s %s: %v", action, target, err)
			writeCatalogError(w, err)
			return
		}
		log.Printf("[modmanager] catalog %s %s: done", action, target)
		onCatalogChange()
		entries, repos, err := cat.List(ctx)
		if err != nil {
			writeCatalogError(w, err)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"entries": entries, "repos": repos})
	}
}

// loopSys is where loopsHandler reads the loop devices; tests point it into a temp dir.
var loopSys = loops.DefaultSysBlock

// loopProc is the /proc loopsHandler reads mount namespaces from; tests point it elsewhere.
var loopProc = loops.DefaultProc

// loopsHandler measures the loop devices old system-extension states hold (internal/loops).
func loopsHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httpx.MethodNotAllowed(w)
			return
		}
		st, err := loops.Measure(loopSys)
		if err != nil {
			httpx.WriteError(w, http.StatusInternalServerError, "internal", err.Error())
			return
		}
		st.StaleUsr = loops.StaleUsr(loopProc)
		httpx.WriteJSON(w, http.StatusOK, st)
	}
}

// progressHandler returns the log of the running or last action (id 0: none yet).
func progressHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httpx.MethodNotAllowed(w)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, jobs.Snapshot())
	}
}

// selfHandler reports whether a newer Module Manager release exists.
func selfHandler(cat catalogService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			httpx.MethodNotAllowed(w)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute)
		defer cancel()
		httpx.WriteJSON(w, http.StatusOK, cat.Self(ctx, version))
	}
}

// selfUpdateHandler downloads and checks the newest release and hands the swap to a
// transient unit; the answer comes before Module Manager goes away for a few seconds.
func selfUpdateHandler(cat catalogService) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			httpx.MethodNotAllowed(w)
			return
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), catalogTimeout)
		defer cancel()
		actionMu.Lock()
		jobs.Start("self-update", version)
		tag, err := cat.SelfUpdate(ctx, version)
		jobs.Finish(err)
		actionMu.Unlock()
		if err != nil {
			log.Printf("[modmanager] self-update from %s: %v", version, err)
			writeCatalogError(w, err)
			return
		}
		log.Printf("[modmanager] self-update %s -> %s handed over", version, tag)
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "updating", "from": version, "to": tag})
	}
}

func writeCatalogError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, catalog.ErrBadRepo):
		httpx.WriteError(w, http.StatusBadRequest, "bad_repo", err.Error())
	case errors.Is(err, catalog.ErrUnknown):
		httpx.WriteError(w, http.StatusNotFound, "unknown_entry", err.Error())
	case errors.Is(err, catalog.ErrNoImage):
		httpx.WriteError(w, http.StatusUnprocessableEntity, "no_image", err.Error())
	case errors.Is(err, catalog.ErrNoChecksum):
		httpx.WriteError(w, http.StatusUnprocessableEntity, "no_checksum", err.Error())
	case errors.Is(err, catalog.ErrChecksum):
		httpx.WriteError(w, http.StatusUnprocessableEntity, "checksum_mismatch", err.Error())
	case errors.Is(err, catalog.ErrOwn):
		httpx.WriteError(w, http.StatusConflict, "own_installer", err.Error())
	case errors.Is(err, catalog.ErrProtected):
		httpx.WriteError(w, http.StatusConflict, "protected", err.Error())
	case errors.Is(err, catalog.ErrCollision):
		httpx.WriteError(w, http.StatusConflict, "collision", err.Error())
	case errors.Is(err, catalog.ErrNoRelease):
		httpx.WriteError(w, http.StatusNotFound, "no_release", err.Error())
	case errors.Is(err, catalog.ErrUnreachable):
		httpx.WriteError(w, http.StatusBadGateway, "unreachable", err.Error())
	case errors.Is(err, catalog.ErrRateLimit):
		httpx.WriteError(w, http.StatusTooManyRequests, "rate_limit", err.Error())
	case errors.Is(err, catalog.ErrNotNewer):
		httpx.WriteError(w, http.StatusConflict, "not_newer", err.Error())
	case errors.Is(err, catalog.ErrNotInstalled):
		httpx.WriteError(w, http.StatusConflict, "not_installed", err.Error())
	default:
		httpx.WriteError(w, http.StatusBadGateway, "failed", err.Error())
	}
}

// onCatalogChange runs after a successful catalog action; main points it at
// the update check, so the dashboard card does not wait 12 hours.
var onCatalogChange = func() {}

// langDir holds the page's language for the dashboard card (internal/updates).
var langDir = defaultLangDir

const defaultLangDir = state.Dir

// uiLangHandler stores the language the page shows ("en", "de" or "fr"): the backend
// has no user token to read the ZimaOS setting itself.
func uiLangHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			httpx.MethodNotAllowed(w)
			return
		}
		var in struct {
			Lang string `json:"lang"`
		}
		if !httpx.Decode(w, r, &in) {
			return
		}
		if err := updates.WriteLang(langDir, in.Lang); err != nil {
			httpx.WriteError(w, http.StatusBadRequest, "bad_lang", err.Error())
			return
		}
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
