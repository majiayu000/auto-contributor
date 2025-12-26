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

// Server provides the web dashboard
type Server struct {
	config  *config.Config
	db      *db.DB
	pool    *worker.Pool
	clients map[chan []byte]bool
	mu      sync.RWMutex
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

// Start begins serving the web interface
func (s *Server) Start() error {
	mux := http.NewServeMux()

	// Static files and dashboard
	mux.HandleFunc("/", s.handleDashboard)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/stats", s.handleStats)
	mux.HandleFunc("/api/issues", s.handleIssues)
	mux.HandleFunc("/events", s.handleSSE)

	addr := fmt.Sprintf(":%d", s.config.WebPort)
	fmt.Printf("Web server starting on %s\n", addr)

	return http.ListenAndServe(addr, mux)
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
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
            background: #0d1117;
            color: #c9d1d9;
            padding: 20px;
        }
        .header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            margin-bottom: 20px;
        }
        h1 { color: #58a6ff; }
        .status-badge {
            padding: 5px 15px;
            border-radius: 20px;
            font-size: 14px;
        }
        .status-running { background: #238636; }
        .status-idle { background: #6e7681; }

        .stats-grid {
            display: grid;
            grid-template-columns: repeat(auto-fit, minmax(200px, 1fr));
            gap: 15px;
            margin-bottom: 30px;
        }
        .stat-card {
            background: #161b22;
            border: 1px solid #30363d;
            border-radius: 8px;
            padding: 20px;
        }
        .stat-value {
            font-size: 32px;
            font-weight: bold;
            color: #58a6ff;
        }
        .stat-label { color: #8b949e; font-size: 14px; }

        .workers-section {
            margin-bottom: 30px;
        }
        .section-title {
            font-size: 18px;
            margin-bottom: 15px;
            color: #f0f6fc;
        }
        .workers-grid {
            display: grid;
            grid-template-columns: repeat(auto-fill, minmax(300px, 1fr));
            gap: 15px;
        }
        .worker-card {
            background: #161b22;
            border: 1px solid #30363d;
            border-radius: 8px;
            padding: 15px;
        }
        .worker-header {
            display: flex;
            justify-content: space-between;
            align-items: center;
            margin-bottom: 10px;
        }
        .worker-id {
            font-weight: bold;
            color: #f0f6fc;
        }
        .worker-status {
            font-size: 12px;
            padding: 3px 10px;
            border-radius: 12px;
        }
        .phase-idle { background: #21262d; color: #8b949e; }
        .phase-cloning { background: #1f6feb; }
        .phase-evaluating { background: #8957e5; }
        .phase-solving { background: #a371f7; }
        .phase-testing { background: #f0883e; }
        .phase-creating_pr { background: #238636; }

        .worker-issue {
            font-size: 13px;
            color: #58a6ff;
            margin: 8px 0;
            white-space: nowrap;
            overflow: hidden;
            text-overflow: ellipsis;
        }
        .progress-bar {
            height: 4px;
            background: #21262d;
            border-radius: 2px;
            overflow: hidden;
            margin: 10px 0;
        }
        .progress-fill {
            height: 100%;
            background: #58a6ff;
            transition: width 0.3s ease;
        }
        .worker-output {
            font-size: 12px;
            color: #8b949e;
            font-family: monospace;
            white-space: nowrap;
            overflow: hidden;
            text-overflow: ellipsis;
        }
        .worker-stats {
            display: flex;
            gap: 15px;
            margin-top: 10px;
            font-size: 12px;
        }
        .worker-stats span { color: #8b949e; }
        .success { color: #3fb950 !important; }
        .failure { color: #f85149 !important; }

        .log-section {
            background: #161b22;
            border: 1px solid #30363d;
            border-radius: 8px;
            padding: 15px;
            max-height: 300px;
            overflow-y: auto;
        }
        .log-entry {
            font-family: monospace;
            font-size: 12px;
            padding: 4px 0;
            border-bottom: 1px solid #21262d;
        }
        .log-time { color: #6e7681; }
        .log-success { color: #3fb950; }
        .log-error { color: #f85149; }
    </style>
</head>
<body>
    <div class="header">
        <h1>Auto-Contributor Dashboard</h1>
        <span id="connection-status" class="status-badge status-idle">Connecting...</span>
    </div>

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
            <div class="stat-label">Completed</div>
        </div>
        <div class="stat-card">
            <div class="stat-value" id="success-rate">0%</div>
            <div class="stat-label">Success Rate</div>
        </div>
    </div>

    <div class="workers-section">
        <div class="section-title">Workers</div>
        <div class="workers-grid" id="workers-grid"></div>
    </div>

    <div class="section-title">Activity Log</div>
    <div class="log-section" id="log-section"></div>

    <script>
        let totalCompleted = 0;
        let totalFailed = 0;

        function updateDashboard(data) {
            // Update stats
            document.getElementById('active-workers').textContent = data.active_workers || 0;
            document.getElementById('queue-size').textContent = data.queue_size || 0;

            // Calculate totals from workers
            let completed = 0, failed = 0;
            if (data.workers) {
                data.workers.forEach(w => {
                    completed += w.tasks_completed || 0;
                    failed += w.tasks_failed || 0;
                });
            }
            document.getElementById('total-completed').textContent = completed;
            const rate = completed + failed > 0 ? Math.round(completed / (completed + failed) * 100) : 0;
            document.getElementById('success-rate').textContent = rate + '%';

            // Update workers grid
            const grid = document.getElementById('workers-grid');
            grid.innerHTML = '';

            if (data.workers) {
                data.workers.forEach(worker => {
                    const card = document.createElement('div');
                    card.className = 'worker-card';

                    const phaseClass = 'phase-' + (worker.phase || 'idle');
                    const issue = worker.current_issue;
                    const issueText = issue ? issue.repo + '#' + issue.issue_number : 'No task';

                    card.innerHTML = ` + "`" + `
                        <div class="worker-header">
                            <span class="worker-id">Worker ${worker.id}</span>
                            <span class="worker-status ${phaseClass}">${worker.phase || 'idle'}</span>
                        </div>
                        <div class="worker-issue">${issueText}</div>
                        <div class="progress-bar">
                            <div class="progress-fill" style="width: ${(worker.progress || 0) * 100}%"></div>
                        </div>
                        <div class="worker-output">${worker.last_output || 'Waiting...'}</div>
                        <div class="worker-stats">
                            <span class="success">✓ ${worker.tasks_completed || 0}</span>
                            <span class="failure">✗ ${worker.tasks_failed || 0}</span>
                        </div>
                    ` + "`" + `;

                    grid.appendChild(card);
                });
            }
        }

        function addLog(message, type = 'info') {
            const log = document.getElementById('log-section');
            const entry = document.createElement('div');
            entry.className = 'log-entry';

            const time = new Date().toLocaleTimeString();
            const typeClass = type === 'success' ? 'log-success' : (type === 'error' ? 'log-error' : '');

            entry.innerHTML = ` + "`" + `<span class="log-time">[${time}]</span> <span class="${typeClass}">${message}</span>` + "`" + `;
            log.insertBefore(entry, log.firstChild);

            // Keep only last 100 entries
            while (log.children.length > 100) {
                log.removeChild(log.lastChild);
            }
        }

        function connect() {
            const status = document.getElementById('connection-status');
            const eventSource = new EventSource('/events');

            eventSource.onopen = function() {
                status.textContent = 'Connected';
                status.className = 'status-badge status-running';
                addLog('Connected to server');
            };

            eventSource.onmessage = function(event) {
                try {
                    const data = JSON.parse(event.data);

                    if (data.type === 'worker_update') {
                        // Single worker update
                        addLog(` + "`" + `Worker ${data.worker.id}: ${data.worker.phase}` + "`" + `);
                    } else {
                        // Full status update
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

        // Initial load
        fetch('/api/status')
            .then(r => r.json())
            .then(updateDashboard)
            .catch(e => addLog('Failed to load initial status', 'error'));

        connect();
    </script>
</body>
</html>`
