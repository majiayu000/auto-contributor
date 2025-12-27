package web

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/majiayu000/auto-contributor/internal/config"
	"github.com/majiayu000/auto-contributor/internal/db"
	"github.com/majiayu000/auto-contributor/internal/worker"
)

// DiscoveryStatus tracks the current discovery phase
type DiscoveryStatus struct {
	IsRunning   bool      `json:"is_running"`
	Phase       string    `json:"phase"`       // "idle", "searching", "analyzing", "complete"
	Topic       string    `json:"topic"`
	StartedAt   time.Time `json:"started_at"`
	Message     string    `json:"message"`
	IssuesFound int       `json:"issues_found"`
}

// Server provides the web dashboard
type Server struct {
	config          *config.Config
	db              *db.DB
	pool            *worker.Pool
	clients         map[chan []byte]bool
	mu              sync.RWMutex
	discoveryStatus DiscoveryStatus
	discoveryMu     sync.RWMutex
}

// New creates a new web server
func New(cfg *config.Config, database *db.DB, pool *worker.Pool) *Server {
	s := &Server{
		config:  cfg,
		db:      database,
		pool:    pool,
		clients: make(map[chan []byte]bool),
	}

	// Set up status callback to broadcast updates
	pool.SetStatusCallback(func(workerID int, status *worker.WorkerStatus) {
		s.broadcast(status)
	})

	return s
}

// UpdateDiscoveryStatus updates the discovery phase status
func (s *Server) UpdateDiscoveryStatus(phase, topic, message string, issuesFound int) {
	s.discoveryMu.Lock()
	defer s.discoveryMu.Unlock()

	if phase == "idle" {
		s.discoveryStatus = DiscoveryStatus{
			IsRunning: false,
			Phase:     "idle",
		}
	} else {
		if !s.discoveryStatus.IsRunning {
			s.discoveryStatus.StartedAt = time.Now()
		}
		s.discoveryStatus.IsRunning = true
		s.discoveryStatus.Phase = phase
		s.discoveryStatus.Topic = topic
		s.discoveryStatus.Message = message
		s.discoveryStatus.IssuesFound = issuesFound
	}

	// Broadcast discovery status update
	s.broadcastDiscoveryStatus()
}

// GetDiscoveryStatus returns current discovery status
func (s *Server) GetDiscoveryStatus() DiscoveryStatus {
	s.discoveryMu.RLock()
	defer s.discoveryMu.RUnlock()
	return s.discoveryStatus
}

func (s *Server) broadcastDiscoveryStatus() {
	status := s.discoveryStatus
	data, _ := json.Marshal(map[string]interface{}{
		"type":      "discovery",
		"discovery": status,
	})
	s.mu.RLock()
	defer s.mu.RUnlock()
	for client := range s.clients {
		select {
		case client <- data:
		default:
		}
	}
}

// Start begins serving the web interface
func (s *Server) Start() error {
	mux := http.NewServeMux()

	// Static files and dashboard
	mux.HandleFunc("/", s.handleDashboard)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/api/issues", s.handleIssues)
	mux.HandleFunc("/api/discovery", s.handleDiscoveryStatus)
	mux.HandleFunc("/events", s.handleSSE)

	addr := fmt.Sprintf(":%d", s.config.WebPort)
	fmt.Printf("Web server starting on %s\n", addr)

	return http.ListenAndServe(addr, mux)
}

// handleDiscoveryStatus returns current discovery status
func (s *Server) handleDiscoveryStatus(w http.ResponseWriter, r *http.Request) {
	status := s.GetDiscoveryStatus()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(status)
}

// handleDashboard serves the main dashboard HTML
func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.Write([]byte(dashboardHTML))
}

// handleStatus returns current worker status
func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	stats := s.pool.GetSystemStats()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// handleStats returns historical statistics
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.db.GetStats(7)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// handleIssues returns recent issues
func (s *Server) handleIssues(w http.ResponseWriter, r *http.Request) {
	// Get issues from different statuses
	discovered, _ := s.db.GetIssuesByStatus("discovered", 10)
	processing, _ := s.db.GetIssuesByStatus("processing", 10)
	prCreated, _ := s.db.GetIssuesByStatus("pr_created", 10)

	result := map[string]interface{}{
		"discovered": discovered,
		"processing": processing,
		"pr_created": prCreated,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

// handleSSE handles Server-Sent Events for real-time updates
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "SSE not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("Access-Control-Allow-Origin", "*")

	// Create client channel
	client := make(chan []byte, 10)

	s.mu.Lock()
	s.clients[client] = true
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.clients, client)
		s.mu.Unlock()
		close(client)
	}()

	// Send initial status
	stats := s.pool.GetSystemStats()
	data, _ := json.Marshal(stats)
	fmt.Fprintf(w, "data: %s\n\n", data)
	flusher.Flush()

	// Send periodic updates
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-client:
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		case <-ticker.C:
			// Send full status update
			stats := s.pool.GetSystemStats()
			data, _ := json.Marshal(stats)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()
		}
	}
}

// broadcast sends a message to all connected clients
func (s *Server) broadcast(status *worker.WorkerStatus) {
	data, err := json.Marshal(map[string]interface{}{
		"type":   "worker_update",
		"worker": status,
	})
	if err != nil {
		return
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	for client := range s.clients {
		select {
		case client <- data:
		default:
			// Client buffer full, skip
		}
	}
}

const dashboardHTML = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Auto-Contributor Dashboard</title>
    <link rel="preconnect" href="https://fonts.googleapis.com">
    <link rel="preconnect" href="https://fonts.gstatic.com" crossorigin>
    <link href="https://fonts.googleapis.com/css2?family=Inter:wght@400;500;600;700&family=JetBrains+Mono:wght@400;500&display=swap" rel="stylesheet">
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }

        :root {
            --bg-primary: #0a0a0f;
            --bg-secondary: #12121a;
            --bg-card: #16161f;
            --bg-hover: #1e1e2a;
            --border: #2a2a3a;
            --text-primary: #f0f0f5;
            --text-secondary: #8888a0;
            --text-muted: #5555666;
            --accent-blue: #3b82f6;
            --accent-purple: #8b5cf6;
            --accent-cyan: #06b6d4;
            --accent-green: #10b981;
            --accent-red: #ef4444;
            --accent-orange: #f59e0b;
            --gradient-1: linear-gradient(135deg, #3b82f6 0%, #8b5cf6 100%);
            --gradient-2: linear-gradient(135deg, #06b6d4 0%, #3b82f6 100%);
            --gradient-3: linear-gradient(135deg, #10b981 0%, #06b6d4 100%);
            --shadow: 0 4px 24px rgba(0, 0, 0, 0.4);
            --shadow-glow: 0 0 40px rgba(59, 130, 246, 0.15);
        }

        body {
            font-family: 'Inter', -apple-system, BlinkMacSystemFont, sans-serif;
            background: var(--bg-primary);
            color: var(--text-primary);
            min-height: 100vh;
            overflow-x: hidden;
        }

        .bg-grid {
            position: fixed;
            inset: 0;
            background-image:
                linear-gradient(rgba(59, 130, 246, 0.03) 1px, transparent 1px),
                linear-gradient(90deg, rgba(59, 130, 246, 0.03) 1px, transparent 1px);
            background-size: 60px 60px;
            pointer-events: none;
        }

        .container {
            max-width: 1600px;
            margin: 0 auto;
            padding: 30px;
            position: relative;
        }

        .header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            margin-bottom: 40px;
            padding-bottom: 30px;
            border-bottom: 1px solid var(--border);
        }

        .logo {
            display: flex;
            align-items: center;
            gap: 16px;
        }

        .logo-icon {
            width: 48px;
            height: 48px;
            background: var(--gradient-1);
            border-radius: 12px;
            display: flex;
            align-items: center;
            justify-content: center;
            font-size: 24px;
            box-shadow: var(--shadow-glow);
        }

        .logo h1 {
            font-size: 24px;
            font-weight: 700;
            background: var(--gradient-1);
            -webkit-background-clip: text;
            -webkit-text-fill-color: transparent;
            background-clip: text;
        }

        .logo span {
            font-size: 13px;
            color: var(--text-secondary);
            font-weight: 400;
        }

        .status-badge {
            display: flex;
            align-items: center;
            gap: 8px;
            padding: 10px 20px;
            border-radius: 100px;
            font-size: 13px;
            font-weight: 500;
            transition: all 0.3s ease;
        }

        .status-badge::before {
            content: '';
            width: 8px;
            height: 8px;
            border-radius: 50%;
            animation: pulse 2s infinite;
        }

        .status-running {
            background: rgba(16, 185, 129, 0.15);
            color: var(--accent-green);
            border: 1px solid rgba(16, 185, 129, 0.3);
        }
        .status-running::before { background: var(--accent-green); }

        .status-idle {
            background: rgba(136, 136, 160, 0.15);
            color: var(--text-secondary);
            border: 1px solid var(--border);
        }
        .status-idle::before { background: var(--text-secondary); animation: none; }

        @keyframes pulse {
            0%, 100% { opacity: 1; transform: scale(1); }
            50% { opacity: 0.5; transform: scale(1.2); }
        }

        .stats-grid {
            display: grid;
            grid-template-columns: repeat(4, 1fr);
            gap: 20px;
            margin-bottom: 40px;
        }

        .stat-card {
            background: var(--bg-card);
            border: 1px solid var(--border);
            border-radius: 16px;
            padding: 24px;
            position: relative;
            overflow: hidden;
            transition: all 0.3s ease;
        }

        .stat-card:hover {
            border-color: var(--accent-blue);
            transform: translateY(-2px);
            box-shadow: var(--shadow);
        }

        .stat-card::before {
            content: '';
            position: absolute;
            top: 0;
            left: 0;
            right: 0;
            height: 3px;
        }

        .stat-card:nth-child(1)::before { background: var(--gradient-1); }
        .stat-card:nth-child(2)::before { background: var(--gradient-2); }
        .stat-card:nth-child(3)::before { background: var(--gradient-3); }
        .stat-card:nth-child(4)::before { background: linear-gradient(135deg, #f59e0b, #ef4444); }

        .stat-icon {
            width: 44px;
            height: 44px;
            border-radius: 12px;
            display: flex;
            align-items: center;
            justify-content: center;
            font-size: 20px;
            margin-bottom: 16px;
        }

        .stat-card:nth-child(1) .stat-icon { background: rgba(59, 130, 246, 0.15); }
        .stat-card:nth-child(2) .stat-icon { background: rgba(6, 182, 212, 0.15); }
        .stat-card:nth-child(3) .stat-icon { background: rgba(16, 185, 129, 0.15); }
        .stat-card:nth-child(4) .stat-icon { background: rgba(245, 158, 11, 0.15); }

        .stat-value {
            font-size: 36px;
            font-weight: 700;
            margin-bottom: 4px;
            font-family: 'JetBrains Mono', monospace;
        }

        .stat-card:nth-child(1) .stat-value { color: var(--accent-blue); }
        .stat-card:nth-child(2) .stat-value { color: var(--accent-cyan); }
        .stat-card:nth-child(3) .stat-value { color: var(--accent-green); }
        .stat-card:nth-child(4) .stat-value { color: var(--accent-orange); }

        .stat-label {
            color: var(--text-secondary);
            font-size: 13px;
            font-weight: 500;
        }

        /* Discovery Status Section */
        .discovery-section {
            margin-bottom: 24px;
        }

        .discovery-card {
            background: var(--bg-card);
            border: 1px solid var(--border);
            border-radius: 16px;
            padding: 20px 24px;
            display: flex;
            align-items: center;
            gap: 20px;
            transition: all 0.3s ease;
        }

        .discovery-card.searching {
            border-color: var(--accent-blue);
            background: linear-gradient(135deg, rgba(59, 130, 246, 0.1) 0%, var(--bg-card) 100%);
        }

        .discovery-card.analyzing {
            border-color: var(--accent-purple);
            background: linear-gradient(135deg, rgba(139, 92, 246, 0.1) 0%, var(--bg-card) 100%);
        }

        .discovery-card.complete {
            border-color: var(--accent-green);
            background: linear-gradient(135deg, rgba(16, 185, 129, 0.1) 0%, var(--bg-card) 100%);
        }

        .discovery-icon {
            width: 56px;
            height: 56px;
            border-radius: 14px;
            background: rgba(136, 136, 160, 0.15);
            display: flex;
            align-items: center;
            justify-content: center;
            color: var(--text-secondary);
            flex-shrink: 0;
        }

        .discovery-card.searching .discovery-icon {
            background: rgba(59, 130, 246, 0.2);
            color: var(--accent-blue);
            animation: pulse-icon 2s infinite;
        }

        .discovery-card.analyzing .discovery-icon {
            background: rgba(139, 92, 246, 0.2);
            color: var(--accent-purple);
            animation: pulse-icon 1.5s infinite;
        }

        .discovery-card.complete .discovery-icon {
            background: rgba(16, 185, 129, 0.2);
            color: var(--accent-green);
        }

        @keyframes pulse-icon {
            0%, 100% { transform: scale(1); opacity: 1; }
            50% { transform: scale(1.05); opacity: 0.8; }
        }

        .discovery-info {
            flex: 1;
        }

        .discovery-title {
            font-size: 16px;
            font-weight: 600;
            margin-bottom: 4px;
        }

        .discovery-message {
            color: var(--text-secondary);
            font-size: 14px;
        }

        .discovery-card.searching .discovery-message,
        .discovery-card.analyzing .discovery-message {
            color: var(--text-primary);
        }

        .discovery-meta {
            text-align: right;
            flex-shrink: 0;
        }

        .discovery-topic {
            font-family: 'JetBrains Mono', monospace;
            font-size: 13px;
            color: var(--accent-cyan);
            margin-bottom: 4px;
            padding: 4px 10px;
            background: rgba(6, 182, 212, 0.15);
            border-radius: 6px;
            display: inline-block;
        }

        .discovery-time {
            font-size: 12px;
            color: var(--text-muted);
            font-family: 'JetBrains Mono', monospace;
        }

        .main-grid {
            display: grid;
            grid-template-columns: 2fr 1fr;
            gap: 24px;
        }

        .section {
            background: var(--bg-card);
            border: 1px solid var(--border);
            border-radius: 16px;
            overflow: hidden;
        }

        .section-header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            padding: 20px 24px;
            border-bottom: 1px solid var(--border);
        }

        .section-title {
            font-size: 16px;
            font-weight: 600;
            display: flex;
            align-items: center;
            gap: 10px;
        }

        .section-title span {
            font-size: 18px;
        }

        .workers-grid {
            display: grid;
            grid-template-columns: repeat(auto-fill, minmax(280px, 1fr));
            gap: 16px;
            padding: 20px;
        }

        .worker-card {
            background: var(--bg-secondary);
            border: 1px solid var(--border);
            border-radius: 12px;
            padding: 16px;
            transition: all 0.3s ease;
        }

        .worker-card:hover {
            border-color: var(--accent-purple);
            background: var(--bg-hover);
        }

        .worker-card.active {
            border-color: var(--accent-green);
            box-shadow: 0 0 20px rgba(16, 185, 129, 0.1);
        }

        .worker-header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            margin-bottom: 12px;
        }

        .worker-id {
            font-weight: 600;
            font-size: 14px;
            display: flex;
            align-items: center;
            gap: 8px;
        }

        .worker-id::before {
            content: '';
            width: 8px;
            height: 8px;
            border-radius: 50%;
            background: var(--text-muted);
        }

        .worker-card.active .worker-id::before {
            background: var(--accent-green);
            animation: pulse 2s infinite;
        }

        .worker-status {
            font-size: 11px;
            font-weight: 600;
            padding: 4px 10px;
            border-radius: 6px;
            text-transform: uppercase;
            letter-spacing: 0.5px;
        }

        .phase-idle { background: var(--bg-primary); color: var(--text-secondary); }
        .phase-cloning { background: rgba(59, 130, 246, 0.2); color: var(--accent-blue); }
        .phase-evaluating { background: rgba(139, 92, 246, 0.2); color: var(--accent-purple); }
        .phase-solving { background: rgba(139, 92, 246, 0.3); color: #a78bfa; }
        .phase-testing { background: rgba(245, 158, 11, 0.2); color: var(--accent-orange); }
        .phase-creating_pr { background: rgba(16, 185, 129, 0.2); color: var(--accent-green); }

        .worker-issue {
            font-size: 13px;
            color: var(--accent-cyan);
            margin-bottom: 12px;
            font-family: 'JetBrains Mono', monospace;
            white-space: nowrap;
            overflow: hidden;
            text-overflow: ellipsis;
        }

        .worker-issue.empty {
            color: var(--text-muted);
            font-family: 'Inter', sans-serif;
            font-style: italic;
        }

        .progress-bar {
            height: 4px;
            background: var(--bg-primary);
            border-radius: 2px;
            overflow: hidden;
            margin-bottom: 12px;
        }

        .progress-fill {
            height: 100%;
            background: var(--gradient-1);
            border-radius: 2px;
            transition: width 0.5s ease;
        }

        .worker-output {
            font-size: 11px;
            color: var(--text-secondary);
            font-family: 'JetBrains Mono', monospace;
            padding: 8px 10px;
            background: var(--bg-primary);
            border-radius: 6px;
            white-space: nowrap;
            overflow: hidden;
            text-overflow: ellipsis;
            margin-bottom: 12px;
        }

        .worker-stats {
            display: flex;
            gap: 16px;
            font-size: 12px;
            font-weight: 500;
        }

        .worker-stats .success { color: var(--accent-green); }
        .worker-stats .failure { color: var(--accent-red); }

        .log-section {
            max-height: 500px;
            overflow-y: auto;
        }

        .log-section::-webkit-scrollbar {
            width: 6px;
        }

        .log-section::-webkit-scrollbar-track {
            background: var(--bg-secondary);
        }

        .log-section::-webkit-scrollbar-thumb {
            background: var(--border);
            border-radius: 3px;
        }

        .log-entry {
            font-family: 'JetBrains Mono', monospace;
            font-size: 12px;
            padding: 12px 20px;
            border-bottom: 1px solid var(--border);
            display: flex;
            gap: 12px;
            transition: background 0.2s;
        }

        .log-entry:hover {
            background: var(--bg-hover);
        }

        .log-time {
            color: var(--text-muted);
            flex-shrink: 0;
        }

        .log-message { color: var(--text-secondary); }
        .log-success .log-message { color: var(--accent-green); }
        .log-error .log-message { color: var(--accent-red); }
        .log-info .log-message { color: var(--accent-cyan); }

        .empty-state {
            padding: 60px 20px;
            text-align: center;
            color: var(--text-secondary);
        }

        .empty-state-icon {
            font-size: 48px;
            margin-bottom: 16px;
            opacity: 0.5;
        }

        @media (max-width: 1200px) {
            .stats-grid { grid-template-columns: repeat(2, 1fr); }
            .main-grid { grid-template-columns: 1fr; }
        }

        @media (max-width: 768px) {
            .container { padding: 16px; }
            .stats-grid { grid-template-columns: 1fr; }
            .header { flex-direction: column; gap: 16px; }
        }
    </style>
</head>
<body>
    <div class="bg-grid"></div>

    <div class="container">
        <header class="header">
            <div class="logo">
                <div class="logo-icon">AC</div>
                <div>
                    <h1>Auto-Contributor</h1>
                    <span>Automated GitHub Issue Solver</span>
                </div>
            </div>
            <div id="connection-status" class="status-badge status-idle">Connecting...</div>
        </header>

        <div class="stats-grid">
            <div class="stat-card">
                <div class="stat-value" id="active-workers">0</div>
                <div class="stat-label">Active Workers</div>
            </div>
            <div class="stat-card">
                <div class="stat-value" id="queue-size">0</div>
                <div class="stat-label">Queue Size</div>
            </div>
            <div class="stat-card">
                <div class="stat-value" id="total-completed">0</div>
                <div class="stat-label">PRs Created</div>
            </div>
            <div class="stat-card">
                <div class="stat-value" id="success-rate">0%</div>
                <div class="stat-label">Success Rate</div>
            </div>
        </div>

        <!-- Discovery Status -->
        <div class="discovery-section" id="discovery-section">
            <div class="discovery-card" id="discovery-card">
                <div class="discovery-icon" id="discovery-icon">
                    <svg width="24" height="24" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2">
                        <circle cx="11" cy="11" r="8"></circle>
                        <path d="m21 21-4.35-4.35"></path>
                    </svg>
                </div>
                <div class="discovery-info">
                    <div class="discovery-title" id="discovery-title">Discovery Status</div>
                    <div class="discovery-message" id="discovery-message">Idle - Waiting for next cycle</div>
                </div>
                <div class="discovery-meta">
                    <div class="discovery-topic" id="discovery-topic"></div>
                    <div class="discovery-time" id="discovery-time"></div>
                </div>
            </div>
        </div>

        <div class="main-grid">
            <div class="section">
                <div class="section-header">
                    <div class="section-title">Workers</div>
                </div>
                <div class="workers-grid" id="workers-grid">
                    <div class="empty-state">
                        <div class="empty-state-icon">...</div>
                        <div>Loading workers...</div>
                    </div>
                </div>
            </div>

            <div class="section">
                <div class="section-header">
                    <div class="section-title">Activity Log</div>
                </div>
                <div class="log-section" id="log-section"></div>
            </div>
        </div>
    </div>

    <!-- Worker Detail Modal -->
    <div class="modal-overlay" id="modal-overlay" onclick="closeModal()">
        <div class="modal" onclick="event.stopPropagation()">
            <div class="modal-header">
                <div class="modal-title" id="modal-title">Worker 0</div>
                <button class="modal-close" onclick="closeModal()">&times;</button>
            </div>
            <div class="modal-body">
                <div class="detail-grid">
                    <div class="detail-item">
                        <div class="detail-label">Status</div>
                        <div class="detail-value" id="detail-status">idle</div>
                    </div>
                    <div class="detail-item">
                        <div class="detail-label">Current Issue</div>
                        <div class="detail-value" id="detail-issue">-</div>
                    </div>
                    <div class="detail-item">
                        <div class="detail-label">Progress</div>
                        <div class="detail-value" id="detail-progress">0%</div>
                    </div>
                    <div class="detail-item">
                        <div class="detail-label">Started</div>
                        <div class="detail-value" id="detail-started">-</div>
                    </div>
                </div>
                <div class="detail-section">
                    <div class="detail-section-title">Live Output</div>
                    <div class="detail-output" id="detail-output"></div>
                </div>
                <div class="detail-section">
                    <div class="detail-section-title">Worker History</div>
                    <div class="detail-history" id="detail-history"></div>
                </div>
            </div>
        </div>
    </div>

    <style>
        .modal-overlay {
            display: none;
            position: fixed;
            inset: 0;
            background: rgba(0, 0, 0, 0.8);
            backdrop-filter: blur(4px);
            z-index: 1000;
            justify-content: center;
            align-items: center;
        }
        .modal-overlay.open { display: flex; }
        .modal {
            background: var(--bg-card);
            border: 1px solid var(--border);
            border-radius: 16px;
            width: 90%;
            max-width: 800px;
            max-height: 85vh;
            overflow: hidden;
            display: flex;
            flex-direction: column;
        }
        .modal-header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            padding: 20px 24px;
            border-bottom: 1px solid var(--border);
        }
        .modal-title {
            font-size: 18px;
            font-weight: 600;
        }
        .modal-close {
            background: none;
            border: none;
            color: var(--text-secondary);
            font-size: 28px;
            cursor: pointer;
            line-height: 1;
            padding: 0 8px;
        }
        .modal-close:hover { color: var(--text-primary); }
        .modal-body {
            padding: 24px;
            overflow-y: auto;
        }
        .detail-grid {
            display: grid;
            grid-template-columns: repeat(4, 1fr);
            gap: 16px;
            margin-bottom: 24px;
        }
        .detail-item {
            background: var(--bg-secondary);
            padding: 16px;
            border-radius: 10px;
        }
        .detail-label {
            font-size: 12px;
            color: var(--text-secondary);
            margin-bottom: 6px;
        }
        .detail-value {
            font-size: 16px;
            font-weight: 600;
            color: var(--accent-cyan);
            font-family: 'JetBrains Mono', monospace;
        }
        .detail-section {
            margin-bottom: 20px;
        }
        .detail-section-title {
            font-size: 14px;
            font-weight: 600;
            color: var(--text-secondary);
            margin-bottom: 12px;
        }
        .detail-output {
            background: var(--bg-primary);
            border: 1px solid var(--border);
            border-radius: 10px;
            padding: 16px;
            font-family: 'JetBrains Mono', monospace;
            font-size: 13px;
            color: var(--accent-green);
            min-height: 120px;
            max-height: 200px;
            overflow-y: auto;
            white-space: pre-wrap;
            word-break: break-all;
        }
        .detail-history {
            background: var(--bg-primary);
            border: 1px solid var(--border);
            border-radius: 10px;
            max-height: 200px;
            overflow-y: auto;
        }
        .history-entry {
            padding: 10px 16px;
            border-bottom: 1px solid var(--border);
            font-size: 12px;
            display: flex;
            gap: 12px;
        }
        .history-entry:last-child { border-bottom: none; }
        .history-time { color: var(--text-muted); flex-shrink: 0; }
        .history-phase {
            padding: 2px 8px;
            border-radius: 4px;
            font-size: 11px;
            font-weight: 500;
        }
        .history-message { color: var(--text-secondary); flex: 1; }
        .worker-card { cursor: pointer; }
    </style>

    <script>
        // Store worker data and history
        let workersData = {};
        let workerHistory = {};
        let selectedWorkerId = null;

        function updateDashboard(data) {
            document.getElementById('active-workers').textContent = data.active_workers || 0;
            document.getElementById('queue-size').textContent = data.queue_size || 0;

            let completed = 0, failed = 0;
            if (data.workers) {
                data.workers.forEach(w => {
                    completed += w.tasks_completed || 0;
                    failed += w.tasks_failed || 0;
                    // Store worker data
                    workersData[w.id] = w;
                    // Track history
                    if (!workerHistory[w.id]) workerHistory[w.id] = [];
                    if (w.phase !== 'idle' && w.last_output) {
                        const lastEntry = workerHistory[w.id][0];
                        if (!lastEntry || lastEntry.output !== w.last_output) {
                            workerHistory[w.id].unshift({
                                time: new Date().toLocaleTimeString(),
                                phase: w.phase,
                                output: w.last_output,
                                issue: w.current_issue ? w.current_issue.repo + '#' + w.current_issue.issue_number : null
                            });
                            if (workerHistory[w.id].length > 50) workerHistory[w.id].pop();
                        }
                    }
                });
            }
            document.getElementById('total-completed').textContent = completed;
            const rate = completed + failed > 0 ? Math.round(completed / (completed + failed) * 100) : 0;
            document.getElementById('success-rate').textContent = rate + '%';

            const grid = document.getElementById('workers-grid');
            grid.innerHTML = '';

            if (data.workers && data.workers.length > 0) {
                data.workers.forEach(worker => {
                    const card = document.createElement('div');
                    const isActive = worker.phase && worker.phase !== 'idle';
                    card.className = 'worker-card' + (isActive ? ' active' : '');
                    card.onclick = () => openWorkerDetail(worker.id);

                    const phaseClass = 'phase-' + (worker.phase || 'idle');
                    const issue = worker.current_issue;
                    const issueText = issue ? issue.repo + '#' + issue.issue_number : '';

                    card.innerHTML = ` + "`" + `
                        <div class="worker-header">
                            <span class="worker-id">Worker ${worker.id}</span>
                            <span class="worker-status ${phaseClass}">${worker.phase || 'idle'}</span>
                        </div>
                        <div class="worker-issue ${issueText ? '' : 'empty'}">${issueText || 'Waiting for task...'}</div>
                        <div class="progress-bar">
                            <div class="progress-fill" style="width: ${(worker.progress || 0) * 100}%"></div>
                        </div>
                        <div class="worker-output">${worker.last_output || '...'}</div>
                        <div class="worker-stats">
                            <span class="success">+ ${worker.tasks_completed || 0}</span>
                            <span class="failure">- ${worker.tasks_failed || 0}</span>
                        </div>
                    ` + "`" + `;

                    grid.appendChild(card);
                });
            } else {
                grid.innerHTML = '<div class="empty-state"><div class="empty-state-icon">...</div><div>No workers available</div></div>';
            }

            // Update modal if open
            if (selectedWorkerId !== null) {
                updateWorkerDetail(selectedWorkerId);
            }
        }

        function openWorkerDetail(workerId) {
            selectedWorkerId = workerId;
            document.getElementById('modal-overlay').classList.add('open');
            updateWorkerDetail(workerId);
        }

        function closeModal() {
            selectedWorkerId = null;
            document.getElementById('modal-overlay').classList.remove('open');
        }

        function updateWorkerDetail(workerId) {
            const worker = workersData[workerId];
            if (!worker) return;

            document.getElementById('modal-title').textContent = 'Worker ' + workerId;
            document.getElementById('detail-status').textContent = worker.phase || 'idle';
            document.getElementById('detail-status').style.color = worker.phase === 'idle' ? 'var(--text-secondary)' : 'var(--accent-green)';

            const issue = worker.current_issue;
            document.getElementById('detail-issue').textContent = issue ? issue.repo + '#' + issue.issue_number : '-';
            document.getElementById('detail-progress').textContent = Math.round((worker.progress || 0) * 100) + '%';

            const startedAt = worker.started_at ? new Date(worker.started_at).toLocaleTimeString() : '-';
            document.getElementById('detail-started').textContent = startedAt;

            document.getElementById('detail-output').textContent = worker.last_output || 'No output yet...';

            // Render history
            const historyEl = document.getElementById('detail-history');
            const history = workerHistory[workerId] || [];
            if (history.length > 0) {
                historyEl.innerHTML = history.map(h => ` + "`" + `
                    <div class="history-entry">
                        <span class="history-time">${h.time}</span>
                        <span class="history-phase phase-${h.phase}">${h.phase}</span>
                        <span class="history-message">${h.issue || ''} ${h.output}</span>
                    </div>
                ` + "`" + `).join('');
            } else {
                historyEl.innerHTML = '<div class="history-entry"><span class="history-message">No history yet</span></div>';
            }
        }

        // Close modal on Escape key
        document.addEventListener('keydown', e => {
            if (e.key === 'Escape') closeModal();
        });

        function addLog(message, type = 'info') {
            const log = document.getElementById('log-section');
            const entry = document.createElement('div');
            entry.className = 'log-entry log-' + type;

            const time = new Date().toLocaleTimeString();
            entry.innerHTML = ` + "`" + `<span class="log-time">${time}</span><span class="log-message">${message}</span>` + "`" + `;
            log.insertBefore(entry, log.firstChild);

            while (log.children.length > 100) {
                log.removeChild(log.lastChild);
            }
        }

        let discoveryStartTime = null;

        function updateDiscoveryStatus(discovery) {
            const card = document.getElementById('discovery-card');
            const title = document.getElementById('discovery-title');
            const message = document.getElementById('discovery-message');
            const topic = document.getElementById('discovery-topic');
            const timeEl = document.getElementById('discovery-time');

            // Remove all phase classes
            card.classList.remove('searching', 'analyzing', 'complete');

            if (!discovery.is_running || discovery.phase === 'idle') {
                title.textContent = 'Discovery Status';
                message.textContent = 'Idle - Waiting for next cycle';
                topic.textContent = '';
                timeEl.textContent = '';
                discoveryStartTime = null;
            } else {
                card.classList.add(discovery.phase);

                if (!discoveryStartTime) {
                    discoveryStartTime = new Date();
                }

                const phaseIcons = {
                    'searching': 'Searching GitHub...',
                    'analyzing': 'Claude Analyzing...',
                    'complete': 'Discovery Complete'
                };

                title.textContent = phaseIcons[discovery.phase] || discovery.phase;
                message.textContent = discovery.message;
                topic.textContent = discovery.topic ? 'Topic: ' + discovery.topic : '';

                // Calculate elapsed time
                if (discoveryStartTime) {
                    const elapsed = Math.floor((new Date() - discoveryStartTime) / 1000);
                    const mins = Math.floor(elapsed / 60);
                    const secs = elapsed % 60;
                    timeEl.textContent = mins > 0 ? mins + 'm ' + secs + 's' : secs + 's';
                }

                // Log phase changes
                if (discovery.phase !== 'idle') {
                    addLog('Discovery: ' + discovery.message, discovery.phase === 'complete' ? 'success' : 'info');
                }
            }
        }

        // Update discovery time every second
        setInterval(() => {
            if (discoveryStartTime) {
                const timeEl = document.getElementById('discovery-time');
                const elapsed = Math.floor((new Date() - discoveryStartTime) / 1000);
                const mins = Math.floor(elapsed / 60);
                const secs = elapsed % 60;
                timeEl.textContent = mins > 0 ? mins + 'm ' + secs + 's' : secs + 's';
            }
        }, 1000);

        function connect() {
            const status = document.getElementById('connection-status');
            const eventSource = new EventSource('/events');

            eventSource.onopen = function() {
                status.textContent = 'Connected';
                status.className = 'status-badge status-running';
                addLog('Connected to dashboard', 'success');
            };

            eventSource.onmessage = function(event) {
                try {
                    const data = JSON.parse(event.data);
                    if (data.type === 'worker_update') {
                        const w = data.worker;
                        workersData[w.id] = w;
                        if (w.phase !== 'idle') {
                            addLog('Worker ' + w.id + ': ' + w.phase, 'info');
                            // Track in history
                            if (!workerHistory[w.id]) workerHistory[w.id] = [];
                            workerHistory[w.id].unshift({
                                time: new Date().toLocaleTimeString(),
                                phase: w.phase,
                                output: w.last_output || '',
                                issue: w.current_issue ? w.current_issue.repo + '#' + w.current_issue.issue_number : null
                            });
                        }
                        // Update modal if showing this worker
                        if (selectedWorkerId === w.id) {
                            updateWorkerDetail(w.id);
                        }
                    } else if (data.type === 'discovery') {
                        updateDiscoveryStatus(data.discovery);
                    } else {
                        updateDashboard(data);
                    }
                } catch (e) {
                    console.error('Parse error:', e);
                }
            };

            eventSource.onerror = function() {
                status.textContent = 'Disconnected';
                status.className = 'status-badge status-idle';
                addLog('Connection lost, reconnecting...', 'error');
                eventSource.close();
                setTimeout(connect, 3000);
            };
        }

        fetch('/api/status')
            .then(r => r.json())
            .then(updateDashboard)
            .catch(e => addLog('Failed to load status', 'error'));

        fetch('/api/discovery')
            .then(r => r.json())
            .then(updateDiscoveryStatus)
            .catch(e => console.log('No discovery status yet'));

        connect();
    </script>
</body>
</html>`
