package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

// ── WebSocket upgrader ──────────────────────────────────────────────────────
var upgrader = websocket.Upgrader{
	HandshakeTimeout: 10 * time.Second,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// ── Gerçek istemci IP ───────────────────────────────────────────────────────
func realIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return xri
	}
	return r.RemoteAddr
}

// ── /healthz ───────────────────────────────────────────────────────────────
func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":   true,
		"time": time.Now().UTC().Format(time.RFC3339),
	})
}

// ── WebSocket handler ───────────────────────────────────────────────────────
func wsHandler(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("[ws] upgrade:", err)
		return
	}
	defer conn.Close()
	log.Printf("[ws] connected ip=%s", realIP(r))

	for {
		msgType, msg, err := conn.ReadMessage()
		if err != nil {
			log.Printf("[ws] disconnected ip=%s err=%v", realIP(r), err)
			return
		}
		reply, _ := json.Marshal(map[string]any{
			"ok":     true,
			"echo":   string(msg),
			"time":   time.Now().UTC().Format(time.RFC3339),
			"client": realIP(r),
		})
		if err := conn.WriteMessage(msgType, reply); err != nil {
			log.Printf("[ws] write err=%v", err)
			return
		}
	}
}

// ── xhttp handler (GET stream / POST) ──────────────────────────────────────
func xhttpHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Transfer-Encoding", "chunked")
		w.Header().Set("X-Accel-Buffering", "no")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		log.Printf("[xhttp] stream start ip=%s", realIP(r))
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		notify := r.Context().Done()
		for {
			select {
			case <-notify:
				log.Printf("[xhttp] stream closed ip=%s", realIP(r))
				return
			case t := <-ticker.C:
				if _, err := w.Write([]byte(t.UTC().Format(time.RFC3339) + "\n")); err != nil {
					return
				}
				flusher.Flush()
			}
		}

	case http.MethodPost:
		w.Header().Set("Content-Type", "application/octet-stream")
		body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			http.Error(w, "read error", http.StatusBadRequest)
			return
		}
		log.Printf("[xhttp] post ip=%s bytes=%d", realIP(r), len(body))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(body)

	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// ── Upstream reverse proxy (/proxy/*) ──────────────────────────────────────
func buildReverseProxy() http.Handler {
	raw := os.Getenv("UPSTREAM_URL")
	if raw == "" {
		raw = "https://45.61.163.95:443" // varsayılan upstream
	}
	target, err := url.Parse(raw)
	if err != nil {
		log.Fatalf("UPSTREAM_URL parse error: %v", err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		log.Printf("[proxy] error: %v", err)
		http.Error(w, "upstream error", http.StatusBadGateway)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/proxy")
		r.Host = target.Host
		log.Printf("[proxy] %s %s → %s", r.Method, r.RequestURI, raw)
		proxy.ServeHTTP(w, r)
	})
}

// ── Root handler: WebSocket veya HTTP ──────────────────────────────────────
func rootHandler(w http.ResponseWriter, r *http.Request) {
	if websocket.IsWebSocketUpgrade(r) {
		wsHandler(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"ok":       true,
		"service":  "cloud-run-ws-xhttp",
		"time":     time.Now().UTC().Format(time.RFC3339),
		"client":   realIP(r),
		"upstream": os.Getenv("UPSTREAM_URL"),
	})
}

// ── main ───────────────────────────────────────────────────────────────────
func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", rootHandler)           // WebSocket + HTTP
	mux.HandleFunc("/ws", wsHandler)           // explicit WebSocket path
	mux.HandleFunc("/xhttp", xhttpHandler)     // xhttp stream / post
	mux.HandleFunc("/healthz", healthHandler)  // health check
	mux.Handle("/proxy/", buildReverseProxy()) // upstream proxy

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 15 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	log.Printf("🚀 listening port=%s upstream=%s", port, os.Getenv("UPSTREAM_URL"))
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
