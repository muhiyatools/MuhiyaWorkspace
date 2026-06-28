package main

import (
	"embed"
	"fmt"
	"log"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"gateway/admin"
	"gateway/db"
	"gateway/proxy"
)

//go:embed static/*
var staticFS embed.FS

var dbReady atomic.Bool

func main() {
	dsn := "host=localhost port=5432 user=postgres password=postgres dbname=gateway sslmode=disable"
	if envDSN := os.Getenv("DATABASE_URL"); envDSN != "" {
		dsn = envDSN
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
		adminPass = "adminpassword"
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
	// DB connection happens in background; /health returns 503 until ready
	go func() {
		log.Printf("Listening on http://localhost:%s...", port)
		if err := http.ListenAndServe(":"+port, pathNormalizationMiddleware(loggerMiddleware(mux))); err != nil {
			log.Fatalf("Server failed to start: %v", err)
		}
	}()

	// Now connect to the external PostgreSQL database (this can take time)
	database, err := connectWithRetry(dsn, 30)
	if err != nil {
		log.Fatalf("Fatal: failed to initialize database: %v", err)
	}
	dbReady.Store(true)
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

	proxyMuxHandler = corsMiddleware(proxy.NewProxyHandler(database, limiter))

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

 [SECURITY CREDENTIALS]
 Username: ` + adminUser + `
 Password: ` + adminPass + `

 Database: External (DATABASE_URL)
==================================================
`)

	// Block main goroutine forever
	select {}
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

func loggerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[REQ] %s %s (x-api-key: %q, Authorization: %q)",
			r.Method, r.URL.Path, r.Header.Get("x-api-key"), r.Header.Get("Authorization"))
		next.ServeHTTP(w, r)
	})
}

func basicAuth(username, password string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, p, ok := r.BasicAuth()
		if !ok || u != username || p != password {
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
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, x-api-key, anthropic-version")

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
