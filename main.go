package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"fmt"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"runtime/debug"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"gateway/admin"
	"gateway/db"
	"gateway/proxy"
)

//go:embed static/*
var staticFS embed.FS

var dbReady atomic.Bool

// buildVersion identifies the running binary. It is "dev" unless injected at
// build time with -ldflags "-X main.buildVersion=009-<yyyymmdd-HHmm>". Exposed
// on /health so a deploy can be verified without guessing which build is live.
var buildVersion = "dev"

// migrationCount is the number of applied DB migrations, loaded once the DB is
// ready and reported on /health alongside the version.
var migrationCount atomic.Int64

func main() {
	dsn := withPostgresConnectTimeout("host=127.0.0.1 port=5432 user=postgres password=postgres dbname=gateway sslmode=disable")
	if envDSN := os.Getenv("DATABASE_URL"); envDSN != "" {
		dsn = withPostgresConnectTimeout(envDSN)
	}

	port := "8090"
	if envPort := os.Getenv("PORT"); envPort != "" {
		port = envPort
	}

	adminUser := os.Getenv("ADMIN_USERNAME")
	if adminUser == "" {
		adminUser = "admin"
	}
	adminPass := os.Getenv("ADMIN_PASSWORD")
	if adminPass == "" {
		if strings.EqualFold(os.Getenv("DEV_MODE"), "1") || strings.EqualFold(os.Getenv("DEV_MODE"), "true") {
			adminPass = "adminpassword"
			log.Printf("[SECURITY] ADMIN_PASSWORD is not set; DEV_MODE is enabled so the insecure default admin password is in use. Never set DEV_MODE in production.")
		} else {
			log.Fatalf("[SECURITY] ADMIN_PASSWORD must be set before starting the gateway (refusing to boot with a guessable default admin password). For local development only, set DEV_MODE=1 to allow the insecure default.")
		}
	}

	// I3/T212: optional SCOPED service credential for the platform (MP). When set,
	// it is accepted ONLY for the /api endpoints MP actually calls (see
	// serviceOrAdminAuth) — never the HTML admin panel. This lets MP stop shipping
	// the human ADMIN credential in its env. Unset = behaviour unchanged.
	svcUser := os.Getenv("SERVICE_USERNAME")
	svcPass := os.Getenv("SERVICE_PASSWORD")
	if svcUser != "" && svcPass != "" {
		log.Printf("[AUTH] Scoped service credential enabled for /api/{users,keys,logs,stats,plans,settings,health}.")
	}

	if raw := strings.TrimSpace(os.Getenv("ADMIN_ALLOWED_ORIGINS")); raw != "" {
		for _, h := range strings.Split(raw, ",") {
			if h = strings.TrimSpace(h); h != "" {
				adminAllowedOrigins = append(adminAllowedOrigins, h)
			}
		}
		log.Printf("[AUTH] Additional admin origins allowed for cross-site writes: %v", adminAllowedOrigins)
	}

	mux := http.NewServeMux()

	// Dynamic handler wrappers so we can register paths immediately at startup
	var apiMuxHandler http.Handler
	var adminMuxHandler http.Handler
	var proxyMuxHandler http.Handler

	// Health endpoint — responds immediately so elest.io proxy never 502s. Also
	// reports the build version + applied-migration count so a deploy is
	// verifiable with a single curl (no more guessing whether a fix is live).
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !dbReady.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprintf(w, `{"status":"connecting","version":%q}`, buildVersion)
			return
		}
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, `{"status":"ok","version":%q,"migrations":%d,"billing_loss":%d}`, buildVersion, migrationCount.Load(), proxy.BillingLossCount.Load())
	})

	// Root redirect (temporary redirect so browsers/CDNs do not permanently cache)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/admin/", http.StatusTemporaryRedirect)
			return
		}
		http.NotFound(w, r)
	})

	// Register API wrapper
	mux.Handle("/api/", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !dbReady.Load() || apiMuxHandler == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error":{"message":"Database is connecting. Please wait.","type":"service_unavailable"}}`))
			return
		}
		apiMuxHandler.ServeHTTP(w, r)
	}))

	// Register Admin wrappers
	dbConnectingHTML := `<!DOCTYPE html>
<html>
<head>
    <title>Database Connecting | MuhiyaLLM</title>
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif; background: #0f172a; color: #f8fafc; display: flex; align-items: center; justify-content: center; height: 100vh; margin: 0; }
        .card { background: #1e293b; padding: 2.5rem; border-radius: 12px; box-shadow: 0 10px 25px rgba(0,0,0,0.3); border: 1px solid #334155; text-align: center; max-width: 500px; }
        h1 { color: #10b981; font-size: 1.8rem; margin-top: 0; }
        p { color: #94a3b8; line-height: 1.6; }
        .spinner { border: 4px solid rgba(255,255,255,0.1); width: 36px; height: 36px; border-radius: 50%; border-left-color: #10b981; animation: spin 1s linear infinite; margin: 1.5rem auto; }
        @keyframes spin { 0% { transform: rotate(0deg); } 100% { transform: rotate(360deg); } }
    </style>
    <meta http-equiv="refresh" content="5">
</head>
<body>
    <div class="card">
        <div class="spinner"></div>
        <h1>Connecting to Database...</h1>
        <p>MuhiyaLLM is currently connecting to your PostgreSQL database. This page will automatically refresh once the connection is established.</p>
        <p><small style="color: #64748b;">Verify your <code>DATABASE_URL</code> environment variable if this takes too long.</small></p>
    </div>
</body>
</html>`

	adminWrapper := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !dbReady.Load() || adminMuxHandler == nil {
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(dbConnectingHTML))
			return
		}
		adminMuxHandler.ServeHTTP(w, r)
	})
	mux.Handle("/admin", adminWrapper)
	mux.Handle("/admin/", adminWrapper)

	// Register Proxy wrappers
	proxyWrapper := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !dbReady.Load() || proxyMuxHandler == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error":{"message":"Database is connecting. Please wait.","type":"service_unavailable"}}`))
			return
		}
		proxyMuxHandler.ServeHTTP(w, r)
	})

	registerProxyRoutes(mux, proxyWrapper)

	// v1 health checks
	mux.Handle("/v1", corsMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"running","gateway":"MuhiyaLLM"}`))
	})))
	mux.HandleFunc("/v1/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/" {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"running","gateway":"MuhiyaLLM"}`))
			return
		}
		http.NotFound(w, r)
	})

	// Start HTTP server immediately so elest.io reverse proxy gets a response
	// DB connection happens in background; /health returns 503 until ready.
	// An explicit *http.Server (rather than the http.ListenAndServe shortcut)
	// gives us header/idle timeouts (Slowloris protection) and lets us drain
	// in-flight streams on shutdown instead of severing them (see the signal
	// handling block below the fmt.Println banner).
	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           recoveryMiddleware(pathNormalizationMiddleware(loggerMiddleware(mux))),
		ReadHeaderTimeout: 10 * time.Second,
		// ReadTimeout bounds how long a client may take to send its BODY.
		// MaxBytesReader bounds the size but not the rate, so without this a
		// client dribbling one byte per second held a goroutine, a connection,
		// and (after auth) a database pool slot indefinitely — classic
		// Slowloris. This is unrelated to WriteTimeout: request bodies here are
		// bounded POSTs even when the response streams for minutes.
		ReadTimeout: 5 * time.Minute,
		IdleTimeout: 120 * time.Second,
		// No WriteTimeout: SSE responses legitimately stream for many minutes.
		// Per-stream stall detection lives in proxy's upstream client instead.
	}
	go func() {
		log.Printf("Listening on http://localhost:%s...", port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Server failed to start: %v", err)
		}
	}()

	// Now connect to the external PostgreSQL database (this can take time)
	database, err := connectWithRetry(dsn, 30)
	if err != nil {
		log.Fatalf("Fatal: failed to initialize database: %v", err)
	}
	defer database.Close()

	// Initialize rate limiter
	limiter := proxy.NewRateLimiter(database)

	// Instantiate actual sub-routers now that DB is connected
	apiMux := http.NewServeMux()
	admin.RegisterRoutes(apiMux, database, limiter)
	apiMuxHandler = serviceOrAdminAuth(adminUser, adminPass, svcUser, svcPass, apiMux)

	adminMux := http.NewServeMux()
	adminMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		if path == "/admin" {
			http.Redirect(w, r, "/admin/", http.StatusTemporaryRedirect)
			return
		}

		if path == "/admin/" {
			data, err := staticFS.ReadFile("static/index.html")
			if err != nil {
				http.Error(w, "Dashboard file index.html not found in embed filesystem", http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "text/html")
			w.Write(data)
			return
		}

		if strings.HasPrefix(path, "/admin/static/") {
			embedPath := strings.TrimPrefix(path, "/admin/")
			data, err := staticFS.ReadFile(embedPath)
			if err != nil {
				http.Error(w, "File not found: "+path, http.StatusNotFound)
				return
			}

			contentType := "text/plain"
			if strings.HasSuffix(path, ".css") {
				contentType = "text/css"
			} else if strings.HasSuffix(path, ".js") {
				contentType = "application/javascript"
			} else if strings.HasSuffix(path, ".html") {
				contentType = "text/html"
			} else if strings.HasSuffix(path, ".png") {
				contentType = "image/png"
			} else if strings.HasSuffix(path, ".svg") {
				contentType = "image/svg+xml"
			}

			w.Header().Set("Content-Type", contentType)
			w.Write(data)
			return
		}

		http.NotFound(w, r)
	})
	adminMuxHandler = basicAuth(adminUser, adminPass, adminMux)

	identitySecret := strings.TrimSpace(os.Getenv("IDENTITY_SECRET"))
	if identitySecret == "" {
		log.Printf("[IDENTITY] IDENTITY_SECRET is not set; stable per-user identity injection is DISABLED (no 'user_id' is added to upstream requests, so all users share one provider cache namespace).")
	} else {
		log.Printf("[IDENTITY] stable per-user identity injection is ENABLED (a derived 'user_id' is added to every DeepSeek request).")
	}

	proxyMuxHandler = corsMiddleware(proxy.NewProxyHandler(database, limiter, identitySecret))

	// Publish readiness only after all sub-handlers are assigned. The atomic
	// Store here happens-before the atomic Load in each request wrapper, so the
	// handler assignments above are guaranteed visible and there is no window
	// where dbReady is true but a handler is still nil.
	dbReady.Store(true)

	// Load the applied-migration count for /health (best-effort; a failure just
	// leaves the reported count at 0).
	if n, err := database.CountMigrations(); err == nil {
		migrationCount.Store(int64(n))
	} else {
		log.Printf("[HEALTH] could not count migrations: %v", err)
	}

	// Optional request_logs retention: unset by default (keeps full audit
	// history), opt in with REQUEST_LOG_RETENTION_DAYS to bound table growth
	// and the cost of every dashboard/budget aggregate query.
	if daysStr := os.Getenv("REQUEST_LOG_RETENTION_DAYS"); daysStr != "" {
		if days, err := strconv.Atoi(daysStr); err == nil && days > 0 {
			superviseLoop("retention-sweeper", func() { runRetentionSweeper(database, time.Duration(days)*24*time.Hour) })
		} else {
			log.Printf("[RETENTION] REQUEST_LOG_RETENTION_DAYS=%q is not a positive integer; retention pruning disabled", daysStr)
		}
	}

	// DB watchdog: boot-time retry alone is not enough - the external
	// PostgreSQL can drop MID-RUN (managed-DB restart, idle NAT reset, host
	// sleep), after which every request used to hang or surface raw query
	// errors until someone manually restarted the gateway. Instead, ping
	// continuously: on consecutive failures flip every route to the existing
	// "Database is connecting" degraded state, keep probing (each ping dials
	// fresh, so the pool heals itself), and flip back the moment the database
	// answers. The gateway now survives any database outage unattended.
	superviseLoop("db-watchdog", func() { watchDatabase(database) })

	fmt.Println(`
    __  ___      __    _               __    __    __  ___
   /  |/  /_  __/ /_  (_)__  ______ _ / /   / /   /  |/  /
  / /|_/ / / / / __ \/ / _ '/ __ '/ // /   / /   / /|_/ / 
 / /  / / /_/ / / / / / /_/ / /_/ / // /___/ /___/ /  / /  
/_/  /_/\',_/_/ /_/_/\',_\',_/_//_____/_____/_/  /_/   
==================================================
 MuhiyaLLM Gateway is running!
 Dashboard: http://localhost:` + port + `/admin/
 OpenAI Endpoint: http://localhost:` + port + `/v1/chat/completions

 [ADMIN LOGIN]
 Username: ` + adminUser + `
 Password: (configured via ADMIN_PASSWORD)

 Database: External (DATABASE_URL)
==================================================
`)

	// Block until a shutdown signal arrives, then drain in-flight requests
	// (including active SSE streams) instead of severing them mid-response -
	// a bare process kill was losing the billing row for whatever request was
	// in flight at the time.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh
	// 120s, not 60s: a redeploy that severs a mid-answer stream costs an agent
	// session its in-flight work, and a long reasoning turn can easily still be
	// streaming a minute in. Draining twice as long makes that much rarer at the
	// cost of a slower deploy.
	const drainWindow = 120 * time.Second
	log.Printf("Shutdown signal received, draining in-flight requests (up to %s)...", drainWindow)
	dbReady.Store(false)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), drainWindow)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("Graceful shutdown did not complete cleanly: %v", err)
	}
	log.Printf("Shutdown complete.")
}

// recoveryMiddleware stops a single handler panic from aborting the
// connection with no client-visible reason. Go's net/http server already
// recovers per-request panics so the PROCESS survives regardless; this adds
// a structured log line and a clean JSON error response instead of an
// abrupt connection close (which a streaming SSE client would otherwise see
// as an opaque network error).
func recoveryMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("[PANIC] %s %s: %v\n%s", r.Method, r.URL.Path, rec, debug.Stack())
				func() {
					// Writing the fallback response can itself fail/panic if
					// the connection already streamed partial output; swallow
					// defensively rather than risk a second unrecovered panic.
					defer func() { _ = recover() }()
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusInternalServerError)
					_, _ = w.Write([]byte(`{"error":{"message":"Internal server error","type":"api_error"}}`))
				}()
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// superviseLoop runs a long-lived background loop under panic recovery and
// restarts it if it dies.
//
// recoveryMiddleware covers HANDLER goroutines only. The process-lifetime loops
// (retention sweeper, DB watchdog, limiter sweeper, billing outbox) run outside
// it, so a panic in one killed it silently for the rest of the process's life.
// The billing outbox is the dangerous case: its death is self-concealing,
// because BillingLossCount stops incrementing too — /health would look HEALTHIER
// while billing quietly stopped being retried.
func superviseLoop(name string, loop func()) {
	go func() {
		for {
			func() {
				defer func() {
					if rec := recover(); rec != nil {
						log.Printf("[PANIC] background loop %s: %v\n%s", name, rec, debug.Stack())
					}
				}()
				loop()
			}()
			// A clean return means the loop finished on purpose (context
			// cancelled at shutdown); only a panic should restart it.
			log.Printf("[BACKGROUND] loop %s exited; restarting in 5s", name)
			time.Sleep(5 * time.Second)
		}
	}()
}

// Degrade after this many consecutive failed pings (one flaky ping must not
// bounce the whole gateway), and recover on the first successful one.
const dbWatchFailureThreshold = 2

func watchDatabase(database *db.DB) {
	const interval = 15 * time.Second
	const pingTimeout = 5 * time.Second
	failures := 0
	for {
		time.Sleep(interval)
		ctx, cancel := context.WithTimeout(context.Background(), pingTimeout)
		err := database.PingContext(ctx)
		cancel()
		if err != nil {
			failures++
			if failures == dbWatchFailureThreshold {
				dbReady.Store(false)
				log.Printf("Database unreachable (%v) - degrading to 'connecting' state until it recovers", err)
			}
			continue
		}
		if failures >= dbWatchFailureThreshold {
			log.Printf("Database recovered - resuming normal service")
		}
		failures = 0
		dbReady.Store(true)
	}
}

// runRetentionSweeper periodically deletes request_logs rows older than
// retention. Runs for the life of the process; a failed prune just logs and
// retries next interval rather than stopping the sweep entirely.
func runRetentionSweeper(database *db.DB, retention time.Duration) {
	const interval = 6 * time.Hour
	for {
		if n, err := database.PruneOldRequestLogs(retention); err != nil {
			log.Printf("[RETENTION] prune failed: %v", err)
		} else if n > 0 {
			log.Printf("[RETENTION] pruned %d request_logs row(s) older than %s", n, retention)
		}
		time.Sleep(interval)
	}
}

func connectWithRetry(dsn string, maxAttempts int) (*db.DB, error) {
	var lastErr error
	for i := 1; i <= maxAttempts; i++ {
		database, err := db.Open(dsn)
		if err == nil {
			if i > 1 {
				log.Printf("Database connected successfully after %d attempts", i)
			}
			return database, nil
		}
		lastErr = err
		log.Printf("Database connection attempt %d/%d failed: %v", i, maxAttempts, err)
		time.Sleep(2 * time.Second)
	}
	return nil, fmt.Errorf("failed to connect to database after %d attempts: %w", maxAttempts, lastErr)
}

// postgresSafetyParams bound how long the SERVER will spend on one statement or
// waiting for one lock. Without them a single slow or blocked query holds its
// pool connection forever: the pool is capped at 25, the billing transaction
// takes a per-user advisory lock, and a handler blocked in the pool queue has no
// deadline of its own (there is deliberately no WriteTimeout, for SSE). One
// user's concurrent streams could therefore stall every other tenant's
// authentication. These make the database refuse rather than hang.
var postgresSafetyParams = map[string]string{
	"connect_timeout":   "5",
	"statement_timeout": "30000", // ms — far above any healthy query here
	"lock_timeout":      "5000",  // ms — the advisory lock is held only briefly
}

func withPostgresConnectTimeout(dsn string) string {
	if strings.TrimSpace(dsn) == "" {
		return dsn
	}

	parsed, err := url.Parse(dsn)
	if err == nil && (parsed.Scheme == "postgres" || parsed.Scheme == "postgresql") {
		query := parsed.Query()
		for name, value := range postgresSafetyParams {
			if query.Get(name) == "" {
				query.Set(name, value)
			}
		}
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}

	// Key/value DSN form.
	trimmed := strings.TrimSpace(dsn)
	for name, value := range postgresSafetyParams {
		if !strings.Contains(trimmed, name) {
			trimmed += " " + name + "=" + value
		}
	}
	return trimmed
}

func registerProxyRoutes(mux *http.ServeMux, proxyWrapper http.Handler) {
	mux.Handle("/v1/chat/completions", proxyWrapper)
	mux.Handle("/chat/completions", proxyWrapper)
	mux.Handle("/v1/completions", proxyWrapper)
	mux.Handle("/completions", proxyWrapper)
	mux.Handle("/v1/messages", proxyWrapper)
	mux.Handle("/messages", proxyWrapper)
	mux.Handle("/v1/audio/transcriptions", proxyWrapper)
	mux.Handle("/audio/transcriptions", proxyWrapper)
	mux.Handle("/v1/models", proxyWrapper)
	mux.Handle("/v1/models/", proxyWrapper)
	mux.Handle("/v1/muhiyacode/models", proxyWrapper)
	mux.Handle("/models", proxyWrapper)
	mux.Handle("/models/", proxyWrapper)
	mux.Handle("/v1/capabilities", proxyWrapper)
	mux.Handle("/capabilities", proxyWrapper)
	mux.Handle("/v1/tools/web_search", proxyWrapper)
	mux.Handle("/tools/web_search", proxyWrapper)
	// 003 (T006): self-service account usage, key-authenticated.
	mux.Handle("/v1/usage", proxyWrapper)
	mux.Handle("/usage", proxyWrapper)
}

func loggerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Never log full credentials. Record only a redacted fingerprint so
		// logs can correlate a caller without leaking the secret.
		log.Printf("[REQ] %s %s (auth: %s)", r.Method, r.URL.Path, redactCredential(r))
		next.ServeHTTP(w, r)
	})
}

// redactCredential returns a non-reversible hint about the presented key.
func redactCredential(r *http.Request) string {
	key := r.Header.Get("x-api-key")
	if key == "" {
		key = strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return "none"
	}
	if len(key) <= 8 {
		return "set(****)"
	}
	return "set(" + key[:4] + "…" + key[len(key)-2:] + ")"
}

func basicAuth(username, password string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		// Constant-time comparison avoids leaking credential length/prefix via
		// response timing.
		userOK := subtle.ConstantTimeCompare([]byte(u), []byte(username)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(p), []byte(password)) == 1
		if !ok || !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="Admin Area"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// serviceAllowlist is the set of /api path prefixes the platform (MP) uses. The
// scoped SERVICE credential is accepted only for these; the human ADMIN
// credential is accepted everywhere.
var serviceAllowlist = []string{
	"/api/users", // users CRUD + /api/users/topups + /api/users/reset-usage
	"/api/keys",  // key issuance / rotation / delete
	"/api/logs",  // usage reads
	"/api/usage-resets",
	"/api/stats", // dashboard stats
	"/api/plans", // plan reads
	"/api/settings",
	"/api/health",
}

func servicePathAllowed(p string) bool {
	for _, pref := range serviceAllowlist {
		if p == pref || strings.HasPrefix(p, pref+"/") {
			return true
		}
	}
	return false
}

// adminAllowedOrigins is an escape hatch for deployments whose reverse proxy
// rewrites Host: set ADMIN_ALLOWED_ORIGINS to the browser-facing host(s).
var adminAllowedOrigins []string

// isStateChangingMethod reports whether a request can mutate state. Reads stay
// unguarded: /api serves no CORS headers, so a cross-site GET's response is
// already unreadable.
func isStateChangingMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// isCrossSiteRequest reports whether a browser drove this request from another
// origin. An ABSENT Origin means a non-browser caller (the platform's server-side
// client, curl, the CLI) and is always allowed: browsers attach Origin to every
// state-changing request, so its absence cannot be forged from a page. Only hosts
// are compared — TLS terminates at the reverse proxy, so r.TLS cannot tell us the
// scheme the browser actually used.
func isCrossSiteRequest(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Host == "" {
		return true
	}
	if strings.EqualFold(parsed.Host, r.Host) {
		return false
	}
	for _, allowed := range adminAllowedOrigins {
		if strings.EqualFold(parsed.Host, allowed) {
			return false
		}
	}
	return true
}

// hasBrowserFormContentType reports whether the body carries one of the three
// content types a browser can send cross-site WITHOUT a preflight. /api returns no
// CORS headers, so a preflight always fails — rejecting exactly these removes the
// last way a page can drive a write (the enctype=text/plain form), while every
// non-browser caller (JSON, or no body at all) is untouched.
func hasBrowserFormContentType(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return false
	}
	switch mediaType {
	case "text/plain", "multipart/form-data", "application/x-www-form-urlencoded":
		return true
	}
	return false
}

// serviceOrAdminAuth accepts the full ADMIN credential for any request, and —
// when a SERVICE credential is configured — also accepts it, but only for the
// allowlisted platform endpoints. Both comparisons are constant-time. When no
// service credential is set this behaves exactly like basicAuth(admin).
func serviceOrAdminAuth(adminUser, adminPass, svcUser, svcPass string, next http.Handler) http.Handler {
	svcEnabled := svcUser != "" && svcPass != ""
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// CSRF: the browser caches Basic-auth credentials per origin and attaches
		// them to cross-site requests, so a page on any other origin could drive a
		// write here (POST /api/logs with a negative cost was the live path). No CORS
		// headers are served on /api, so a preflight always fails — which leaves
		// exactly two ways in, and this closes both. 403 rather than 401: a
		// WWW-Authenticate challenge here would pop a login prompt at the victim.
		if isStateChangingMethod(r.Method) && (isCrossSiteRequest(r) || hasBrowserFormContentType(r)) {
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}
		u, p, ok := r.BasicAuth()
		if ok {
			adminOK := subtle.ConstantTimeCompare([]byte(u), []byte(adminUser)) == 1 &&
				subtle.ConstantTimeCompare([]byte(p), []byte(adminPass)) == 1
			if adminOK {
				next.ServeHTTP(w, r)
				return
			}
			if svcEnabled {
				svcOK := subtle.ConstantTimeCompare([]byte(u), []byte(svcUser)) == 1 &&
					subtle.ConstantTimeCompare([]byte(p), []byte(svcPass)) == 1
				if svcOK && servicePathAllowed(r.URL.Path) {
					next.ServeHTTP(w, r)
					return
				}
			}
		}
		w.Header().Set("WWW-Authenticate", `Basic realm="Admin Area"`)
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	})
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
		// X-Muhiya-Session and X-Client-App are load-bearing, not decorative: the
		// first pins a conversation's model so provider prefix caches stay warm,
		// the second gates model visibility and the cost chunk. A browser client
		// that cannot send them silently loses both.
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, x-api-key, anthropic-version, X-Muhiya-Effort, X-Muhiya-Session, X-Session-Id, X-Muhiya-Request-ID, X-Muhiya-Attempt, X-Muhiya-Cache-Epoch, X-Muhiya-Expected-Model-Record, X-Muhiya-Expected-Target-Model, X-Muhiya-Compatibility-Epoch, X-Client-App")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func pathNormalizationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/v1/") {
			r.URL.Path = strings.Replace(r.URL.Path, "/v1/v1/", "/v1/", 1)
		}
		next.ServeHTTP(w, r)
	})
}
