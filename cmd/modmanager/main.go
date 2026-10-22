// Command modmanager is the backend of the Module Manager for ZimaOS: it
// hides and restores the built-in apps (internal/sysapps), serves the UI
// and its JSON API behind the ZimaOS gateway and installs its own boot
// watchdog units.
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chicohaager/lintux-modkit/auth"
	"github.com/chicohaager/lintux-modkit/gateway"
	"github.com/chicohaager/lintux-modkit/httpx"
	"github.com/chicohaager/lintux-modkit/watchdog"
	"github.com/chicohaager/modmanager/internal/catalog"
	"github.com/chicohaager/modmanager/internal/sysapps"
)

const (
	version            = "0.2.4"
	routePrefix        = "/modmanager"
	staticDir          = "/usr/share/casaos/www/modules/modmanager"
	defaultRuntimePath = "/var/run/casaos"
	maxRequestBody     = 64 << 10
	actionTimeout      = 2 * time.Minute // systemd-sysext refresh unmerges and remerges all of /usr
)

func main() {
	log.Printf("[modmanager] starting v%s", version)
	if err := watchdog.Install(watchdog.Options{Service: "modmanager", Binary: "/usr/bin/modmanager"}); err != nil {
		log.Printf("[modmanager] %v", err)
	}
	runtimePath := defaultRuntimePath
	if p := os.Getenv("CASAOS_RUNTIME_PATH"); p != "" {
		runtimePath = p
	}

	mgr := sysapps.New()
	go reconcile(mgr)

	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "tcp", net.JoinHostPort("127.0.0.1", "0"))
	if err != nil {
		log.Fatal(err)
	}
	log.Printf("[modmanager] listening on http://%s", listener.Addr())
	go registerRoute(runtimePath, "http://"+listener.Addr().String())

	srv := &http.Server{
		Handler:           withStatic(httpx.CSRF(newMux(mgr, catalog.New(sysapps.ExecRunner), newVerifier(runtimePath).Middleware))),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go shutdownOnSignal(srv)
	if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// reconcile rewrites stale shadow manifests after an OS update. /DATA and
// the sysext merge may be late at boot, so it retries for two minutes.
func reconcile(mgr *sysapps.Manager) {
	for i := 1; i <= 24; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		changed, err := mgr.Reconcile(ctx)
		cancel()
		if err == nil {
			if len(changed) > 0 {
				log.Printf("[modmanager] reconcile: rewrote shadow manifest of %v", changed)
			}
			return
		}
		log.Printf("[modmanager] reconcile attempt %d: %v", i, err)
		time.Sleep(5 * time.Second)
	}
}

// newVerifier: MODMANAGER_DISABLE_AUTH=1 is a development switch only; the
// shipped unit never sets it.
func newVerifier(runtimePath string) *auth.Verifier {
	if os.Getenv("MODMANAGER_DISABLE_AUTH") == "1" {
		log.Printf("[modmanager] WARNING: authentication disabled by MODMANAGER_DISABLE_AUTH")
		return auth.Disabled()
	}
	return auth.NewVerifier(auth.JWKSResolver(runtimePath))
}

func withStatic(next http.Handler) http.Handler {
	dir := staticDir
	if env := os.Getenv("MODMANAGER_STATIC_DIR"); env != "" {
		dir = env
	}
	return httpx.Static("/modules/modmanager/", dir, next)
}

// registerRoute retries for two minutes: the gateway needs ~45 s after boot
// and a route posted before it listens is lost (measured for cron on 1.7.1).
func registerRoute(runtimePath, target string) {
	for i := 1; i <= 60; i++ {
		err := gateway.Register(context.Background(), runtimePath, routePrefix, target, 10*time.Second)
		if err == nil {
			log.Printf("[modmanager] gateway route registered: %s -> %s", routePrefix, target)
			return
		}
		log.Printf("[modmanager] gateway route attempt %d: %v", i, err)
		time.Sleep(2 * time.Second)
	}
	log.Printf("[modmanager] gave up registering the gateway route")
}

func shutdownOnSignal(srv *http.Server) {
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT)
	log.Printf("[modmanager] received %v, shutting down", <-ch)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}
