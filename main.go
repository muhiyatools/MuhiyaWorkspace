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
	// 1. Initialize PostgreSQL Database with retry
	dsn := "host=localhost port=5432 user=postgres password=postgres dbname=gateway sslmode=disable"
	if envDSN := os.Getenv("DATABASE_URL"); envDSN != "" {
		dsn = envDSN
	}

	database, err := connectWithRetry(dsn, 30)
	if err != nil {
		log.Fatalf("Fatal: failed to initialize database: %v", err)
	}
	dbReady.Store(true)
	defer database.Close()

	// 2. Initialize Rate Limiter
	limiter := proxy.NewRateLimiter(database)

	// 3. Setup Routing & Security Mappings
	adminUser := os.Getenv("ADMIN_USERNAME")
	if adminUser == "" {
		adminUser = "admin"
	}
	adminPass := os.Getenv("ADMIN_PASSWORD")
	if adminPass == "" {
		adminPass = "adminpassword"
	}

	mux := http.NewServeMux()

	// Health check — used by Docker HEALTHCHECK and elest.io
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		if !dbReady.Load() {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"status":"not_ready"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	})

	// Redirect root / to /admin/
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			http.Redirect(w, r, "/admin/", http.StatusMovedPermanently)
			return
		}
		http.NotFound(w, r)
	})

	// Health check for /v1 and /v1/ (crucial for client-side ping/verifications)
	mux.HandleFunc("/v1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"running","gateway":"MuhiyaLLM"}`))
	})
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

	// Setup API Mux protected by Basic Auth
	apiMux := http.NewServeMux()
	admin.RegisterRoutes(apiMux, database)
	mux.Handle("/api/", basicAuth(adminUser, adminPass, apiMux))

	// Setup Admin Interface Mux
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

	// Apply Basic Auth to Admin UI endpoints
	mux.Handle("/admin", basicAuth(adminUser, adminPass, adminMux))
	mux.Handle("/admin/", basicAuth(adminUser, adminPass, adminMux))

	// Register Proxy Handler (Unprotected by Basic Auth - uses Bearer key)
	proxyHandler := proxy.NewProxyHandler(database, limiter)
	mux.Handle("/v1/chat/completions", corsMiddleware(proxyHandler))
	mux.Handle("/chat/completions", corsMiddleware(proxyHandler))
	mux.Handle("/v1/completions", corsMiddleware(proxyHandler))
	mux.Handle("/completions", corsMiddleware(proxyHandler))
	mux.Handle("/v1/messages", corsMiddleware(proxyHandler))
	mux.Handle("/messages", corsMiddleware(proxyHandler))
	mux.Handle("/v1/audio/transcriptions", corsMiddleware(proxyHandler))
	mux.Handle("/audio/transcriptions", corsMiddleware(proxyHandler))
	mux.Handle("/v1/models", corsMiddleware(proxyHandler))
	mux.Handle("/v1/models/", corsMiddleware(proxyHandler))
	mux.Handle("/models", corsMiddleware(proxyHandler))
	mux.Handle("/models/", corsMiddleware(proxyHandler))

	// 4. Start HTTP Server
	port := "8090"
	if envPort := os.Getenv("PORT"); envPort != "" {
		port = envPort
	}

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

	log.Printf("Listening on http://localhost:%s...", port)
	if err := http.ListenAndServe(":"+port, pathNormalizationMiddleware(loggerMiddleware(mux))); err != nil {
		log.Fatalf("Server failed to start: %v", err)
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

func loggerMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Printf("[REQ] %s %s (x-api-key: %q, Authorization: %q)", 
			r.Method, r.URL.Path, r.Header.Get("x-api-key"), r.Header.Get("Authorization"))
		next.ServeHTTP(w, r)
	})
}

// Basic Authentication Middleware
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

// Simple CORS middleware to allow external developer tools/SDKs to call the local endpoint
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

// Path normalization middleware to dynamically fix client URL duplication issues (e.g. /v1/v1/)
func pathNormalizationMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v1/v1/") {
			r.URL.Path = strings.Replace(r.URL.Path, "/v1/v1/", "/v1/", 1)
		}
		next.ServeHTTP(w, r)
	})
}
