package transport

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/gateway-fm/loadgenerator/pkg/types"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool {
		origin := r.Header.Get("Origin")
		if origin == "" {
			return true // Allow requests without Origin header (same-origin or direct)
		}

		// Parse the origin URL
		originURL, err := url.Parse(origin)
		if err != nil {
			return false
		}

		// Allow same origin (same host)
		if originURL.Host == r.Host {
			return true
		}

		// Allow localhost connections (common for development)
		if originURL.Hostname() == "localhost" || originURL.Hostname() == "127.0.0.1" {
			return true
		}

		return false
	},
}

// WebSocketServer handles WebSocket connections for real-time metrics streaming.
type WebSocketServer struct {
	api    LoadGeneratorAPI
	logger *slog.Logger

	// Connected clients
	clients   map[*websocket.Conn]bool
	clientsMu sync.RWMutex

	// Broadcast channel
	broadcast chan types.TestMetrics

	// Done channel for shutdown
	done chan struct{}
}

// NewWebSocketServer creates a new WebSocket server.
func NewWebSocketServer(api LoadGeneratorAPI, logger *slog.Logger) *WebSocketServer {
	if logger == nil {
		logger = slog.Default()
	}
	return &WebSocketServer{
		api:       api,
		logger:    logger,
		clients:   make(map[*websocket.Conn]bool),
		broadcast: make(chan types.TestMetrics, 10),
		done:      make(chan struct{}),
	}
}

// Handler returns the WebSocket HTTP handler.
func (ws *WebSocketServer) Handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			ws.logger.Error("WebSocket upgrade failed", slog.String("error", err.Error()))
			return
		}

		// Register client
		ws.clientsMu.Lock()
		ws.clients[conn] = true
		ws.clientsMu.Unlock()

		ws.logger.Debug("WebSocket client connected",
			slog.Int("total_clients", len(ws.clients)),
		)

		// Handle client disconnect
		defer func() {
			ws.clientsMu.Lock()
			delete(ws.clients, conn)
			ws.clientsMu.Unlock()
			conn.Close()

			ws.logger.Debug("WebSocket client disconnected",
				slog.Int("total_clients", len(ws.clients)),
			)
		}()

		// Read messages (mainly for ping/pong)
		for {
			_, _, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					ws.logger.Debug("WebSocket read error", slog.String("error", err.Error()))
				}
				break
			}
		}
	}
}

// Start begins the metrics broadcasting goroutine.
func (ws *WebSocketServer) Start() {
	go ws.broadcastLoop()
}

// Stop stops the WebSocket server.
func (ws *WebSocketServer) Stop() {
	close(ws.done)

	// Close all client connections
	ws.clientsMu.Lock()
	for conn := range ws.clients {
		conn.Close()
	}
	ws.clients = make(map[*websocket.Conn]bool)
	ws.clientsMu.Unlock()
}

// broadcastLoop continuously broadcasts metrics to all connected clients.
func (ws *WebSocketServer) broadcastLoop() {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ws.done:
			return
		case <-ticker.C:
			metrics := ws.api.GetMetrics()

			// Broadcast if test is active (initializing/running/verifying/error/completed) or was recently active
			if metrics.Status == types.StatusInitializing || metrics.Status == types.StatusRunning ||
				metrics.Status == types.StatusVerifying || metrics.Status == types.StatusError ||
				metrics.Status == types.StatusCompleted || metrics.TxSent > 0 {
				ws.broadcastMetrics(metrics)
			}
		}
	}
}

// broadcastMetrics sends metrics to all connected clients.
func (ws *WebSocketServer) broadcastMetrics(metrics types.TestMetrics) {
	data, err := json.Marshal(metrics)
	if err != nil {
		ws.logger.Error("Failed to marshal metrics", slog.String("error", err.Error()))
		return
	}

	ws.clientsMu.RLock()
	defer ws.clientsMu.RUnlock()

	for conn := range ws.clients {
		err := conn.WriteMessage(websocket.TextMessage, data)
		if err != nil {
			ws.logger.Debug("Failed to write to WebSocket",
				slog.String("error", err.Error()),
			)
			// Will be cleaned up by the read loop
		}
	}
}

// ClientCount returns the number of connected clients.
func (ws *WebSocketServer) ClientCount() int {
	ws.clientsMu.RLock()
	defer ws.clientsMu.RUnlock()
	return len(ws.clients)
}
