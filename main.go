package main

import (
	"context"
	"crypto/subtle"
	"embed"
	"fmt"
	"log"
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

	mux := http.NewServeMux()

	// Dynamic handler wrappers so we can register paths immediately at startup
	var apiMuxHandler http.Handler
	var adminMuxHandler http.Handler
	var proxyMuxHandler http.Handler

	// Health endpoint — responds immediately so elest.io proxy never 502s
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if !dbReady.Load() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"status":"connecting"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Root redirect
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
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
		IdleTimeout:       120 * time.Second,
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
	admin.RegisterRoutes(apiMux, database)
	apiMuxHandler = basicAuth(adminUser, adminPass, apiMux)

	adminMux := http.NewServeMux()
	adminMux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		if path == "/admin" {
			http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
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
		log.Printf("[IDENTITY] IDENTITY_SECRET is not set; stable user identity injection is DISABLED (no 'user' field is added to upstream requests).")
	}

	proxyMuxHandler = corsMiddleware(proxy.NewProxyHandler(database, limiter, identitySecret))

	// Publish readiness only after all sub-handlers are assigned. The atomic
	// Store here happens-before the atomic Load in each request wrapper, so the
	// handler assignments above are guaranteed visible and there is no window
	// where dbReady is true but a handler is still nil.
	dbReady.Store(true)

	// Optional request_logs retention: unset by default (keeps full audit
	// history), opt in with REQUEST_LOG_RETENTION_DAYS to bound table growth
	// and the cost of every dashboard/budget aggregate query.
	if daysStr := os.Getenv("REQUEST_LOG_RETENTION_DAYS"); daysStr != "" {
		if days, err := strconv.Atoi(daysStr); err == nil && days > 0 {
			go runRetentionSweeper(database, time.Duration(days)*24*time.Hour)
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
	go watchDatabase(database)

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
	log.Printf("Shutdown signal received, draining in-flight requests (up to 60s)...")
	dbReady.Store(false)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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

func withPostgresConnectTimeout(dsn string) string {
	if strings.TrimSpace(dsn) == "" || strings.Contains(dsn, "connect_timeout") {
		return dsn
	}

	parsed, err := url.Parse(dsn)
	if err == nil && (parsed.Scheme == "postgres" || parsed.Scheme == "postgresql") {
		query := parsed.Query()
		if query.Get("connect_timeout") == "" {
			query.Set("connect_timeout", "5")
			parsed.RawQuery = query.Encode()
		}
		return parsed.String()
	}

	return strings.TrimSpace(dsn) + " connect_timeout=5"
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

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "POST, GET, OPTIONS, PUT, DELETE")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, x-api-key, anthropic-version, X-Muhiya-Effort")

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
