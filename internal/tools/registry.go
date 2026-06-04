package tools

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"micro-claw/internal/config"
	"micro-claw/internal/telegram"
)

type FunctionParameter struct {
	Type        string                       `json:"type"`
	Description string                       `json:"description,omitempty"`
	Properties  map[string]FunctionParameter `json:"properties,omitempty"`
	Required    []string                     `json:"required,omitempty"`
}

type FunctionDeclaration struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Parameters  FunctionParameter `json:"parameters"`
}

type Tool struct {
	FunctionDeclarations []FunctionDeclaration `json:"functionDeclarations"`
}

type ToolDefinition struct {
	Declaration FunctionDeclaration
	IsStateful  bool // true if it requires user approval
	Execute     func(ctx context.Context, args map[string]interface{}) (string, error)
}

type ScheduledTask struct {
	Name     string
	Interval time.Duration
	Command  string
	Cancel   context.CancelFunc
}

type Registry struct {
	cfg            *config.Config
	tgClient       *telegram.Client
	chatID         int64
	tools          map[string]ToolDefinition
	pendingActions map[string]chan bool
	pendingMsgs    map[string]int64 // maps actionID to telegram messageID
	mutex          sync.Mutex

	// In-memory scheduler fields
	schedulerTasks map[string]*ScheduledTask
	schedulerMutex sync.Mutex
}

func NewRegistry(cfg *config.Config, tgClient *telegram.Client, chatID int64) *Registry {
	r := &Registry{
		cfg:            cfg,
		tgClient:       tgClient,
		chatID:         chatID,
		tools:          make(map[string]ToolDefinition),
		pendingActions: make(map[string]chan bool),
		pendingMsgs:    make(map[string]int64),
		schedulerTasks: make(map[string]*ScheduledTask),
	}
	r.registerAllTools()
	return r
}

func (r *Registry) GetToolsSchema() []Tool {
	var decls []FunctionDeclaration
	for _, t := range r.tools {
		decls = append(decls, t.Declaration)
	}
	return []Tool{
		{FunctionDeclarations: decls},
	}
}

func (r *Registry) HasTool(name string) bool {
	_, ok := r.tools[name]
	return ok
}

func (r *Registry) UnscheduleAll() {
	r.schedulerMutex.Lock()
	defer r.schedulerMutex.Unlock()
	for name, task := range r.schedulerTasks {
		log.Printf("[Scheduler] Stopping scheduled task: %s", name)
		task.Cancel()
	}
	r.schedulerTasks = make(map[string]*ScheduledTask)
}

func (r *Registry) Execute(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	t, ok := r.tools[name]
	if !ok {
		return "", fmt.Errorf("tool %s not found", name)
	}

	if !t.IsStateful {
		// Read-only tool, execute immediately
		log.Printf("[Tools] Executing read-only tool: %s with args: %+v", name, args)
		return t.Execute(ctx, args)
	}

	// State-changing tool, requires Telegram confirmation
	log.Printf("[Tools] Requesting authorization for state-changing tool: %s with args: %+v", name, args)

	actionID := generateActionID()
	resChan := make(chan bool, 1)

	r.mutex.Lock()
	r.pendingActions[actionID] = resChan
	r.mutex.Unlock()

	// Format authorization message
	authMsg := fmt.Sprintf(
		"⚠️ *Action Authorization Required*\n\n*Tool:* `%s`\n*Arguments:* `%+v`\n\nDo you want to authorize this action?",
		name, args,
	)

	// Send message with inline keyboard
	msg, err := r.tgClient.SendApprovalKeyboard(ctx, r.chatID, authMsg, actionID)
	if err != nil {
		r.mutex.Lock()
		delete(r.pendingActions, actionID)
		r.mutex.Unlock()
		return "", fmt.Errorf("failed to send Telegram approval keyboard: %w", err)
	}

	r.mutex.Lock()
	r.pendingMsgs[actionID] = msg.MessageID
	r.mutex.Unlock()

	// Wait for user callback approval, timeout or context cancellation
	select {
	case approved := <-resChan:
		r.mutex.Lock()
		msgID := r.pendingMsgs[actionID]
		delete(r.pendingActions, actionID)
		delete(r.pendingMsgs, actionID)
		r.mutex.Unlock()

		if approved {
			// Update Telegram UI to show Approved status
			approvedText := fmt.Sprintf("✅ *Approved & Executing:* `%s` with args `%+v`", name, args)
			_ = r.tgClient.EditMessageText(ctx, r.chatID, msgID, approvedText)

			log.Printf("[Tools] Action %s approved by user. Executing...", actionID)
			return t.Execute(ctx, args)
		} else {
			// Update Telegram UI to show Rejected status
			rejectedText := fmt.Sprintf("❌ *Rejected:* `%s` with args `%+v`", name, args)
			_ = r.tgClient.EditMessageText(ctx, r.chatID, msgID, rejectedText)

			log.Printf("[Tools] Action %s rejected by user.", actionID)
			return "Action rejected by user. Command was NOT executed.", nil
		}

	case <-time.After(time.Duration(r.cfg.UserApprovalTimeoutMinutes) * time.Minute):
		r.mutex.Lock()
		msgID, ok := r.pendingMsgs[actionID]
		delete(r.pendingActions, actionID)
		delete(r.pendingMsgs, actionID)
		r.mutex.Unlock()

		if ok {
			timeoutText := fmt.Sprintf("⏰ *Timed Out:* `%s` with args `%+v` (no response in %d minutes)", name, args, r.cfg.UserApprovalTimeoutMinutes)
			_ = r.tgClient.EditMessageText(ctx, r.chatID, msgID, timeoutText)
		}
		return "Action timed out waiting for approval. Command was NOT executed.", nil

	case <-ctx.Done():
		r.mutex.Lock()
		msgID, ok := r.pendingMsgs[actionID]
		delete(r.pendingActions, actionID)
		delete(r.pendingMsgs, actionID)
		r.mutex.Unlock()

		if ok {
			cancelledText := fmt.Sprintf("🛑 *Cancelled:* `%s` with args `%+v` due to daemon shutdown", name, args)
			_ = r.tgClient.EditMessageText(ctx, r.chatID, msgID, cancelledText)
		}
		return "Action cancelled due to context termination.", ctx.Err()
	}
}

func (r *Registry) HandleCallback(ctx context.Context, cb *telegram.CallbackQuery) {
	data := cb.Data
	// data is in format "approve:<actionID>" or "reject:<actionID>"
	var approved bool
	var actionID string

	if len(data) > 8 && data[:8] == "approve:" {
		approved = true
		actionID = data[8:]
	} else if len(data) > 7 && data[:7] == "reject:" {
		approved = false
		actionID = data[7:]
	} else {
		log.Printf("[Tools] Unknown callback format: %s", data)
		_ = r.tgClient.AnswerCallbackQuery(ctx, cb.ID, "Invalid action format")
		return
	}

	r.mutex.Lock()
	resChan, exists := r.pendingActions[actionID]
	r.mutex.Unlock()

	if !exists {
		log.Printf("[Tools] Action ID not found or already processed: %s", actionID)
		_ = r.tgClient.AnswerCallbackQuery(ctx, cb.ID, "Action has expired or already been handled")
		return
	}

	// Unblock execution loop
	resChan <- approved

	// Answer callback to remove loading state
	statusText := "Action Approved"
	if !approved {
		statusText = "Action Rejected"
	}
	_ = r.tgClient.AnswerCallbackQuery(ctx, cb.ID, statusText)
}

func (r *Registry) registerAllTools() {
	// 1. get_docker_stats
	r.tools["get_docker_stats"] = ToolDefinition{
		Declaration: FunctionDeclaration{
			Name:        "get_docker_stats",
			Description: "Get CPU, memory, and network utilization for all running Docker containers",
			Parameters: FunctionParameter{
				Type:       "OBJECT",
				Properties: map[string]FunctionParameter{},
			},
		},
		IsStateful: false,
		Execute: func(ctx context.Context, args map[string]interface{}) (string, error) {
			return runCmd("docker", "stats", "--no-stream", "--format", "table {{.Name}}\t{{.CPUPerc}}\t{{.MemUsage}}\t{{.MemPerc}}\t{{.NetIO}}")
		},
	}

	// 2. get_container_logs
	r.tools["get_container_logs"] = ToolDefinition{
		Declaration: FunctionDeclaration{
			Name:        "get_container_logs",
			Description: "Get logs from a specific Docker container",
			Parameters: FunctionParameter{
				Type: "OBJECT",
				Properties: map[string]FunctionParameter{
					"name": {
						Type:        "STRING",
						Description: "The name or ID of the Docker container",
					},
					"tail": {
						Type:        "INTEGER",
						Description: "Number of lines to show from the end of the logs (default 30)",
					},
				},
				Required: []string{"name"},
			},
		},
		IsStateful: false,
		Execute: func(ctx context.Context, args map[string]interface{}) (string, error) {
			name, ok := args["name"].(string)
			if !ok || name == "" {
				return "", fmt.Errorf("missing or invalid 'name' argument")
			}
			tail := "30"
			if tVal, ok := args["tail"]; ok {
				// float64 from JSON unmarshaling
				if fVal, ok := tVal.(float64); ok {
					tail = fmt.Sprintf("%d", int(fVal))
				}
			}
			return runCmd("docker", "logs", "--tail", tail, name)
		},
	}

	// 3. check_zpool_status
	r.tools["check_zpool_status"] = ToolDefinition{
		Declaration: FunctionDeclaration{
			Name:        "check_zpool_status",
			Description: "Get the status of ZFS pools on the host NAS",
			Parameters: FunctionParameter{
				Type:       "OBJECT",
				Properties: map[string]FunctionParameter{},
			},
		},
		IsStateful: false,
		Execute: func(ctx context.Context, args map[string]interface{}) (string, error) {
			return runCmd("zpool", "status")
		},
	}

	// 4. get_disk_usage
	r.tools["get_disk_usage"] = ToolDefinition{
		Declaration: FunctionDeclaration{
			Name:        "get_disk_usage",
			Description: "Get disk space utilization (df -h) on the host NAS",
			Parameters: FunctionParameter{
				Type:       "OBJECT",
				Properties: map[string]FunctionParameter{},
			},
		},
		IsStateful: false,
		Execute: func(ctx context.Context, args map[string]interface{}) (string, error) {
			return runCmd("df", "-h")
		},
	}

	// 5. restart_container
	r.tools["restart_container"] = ToolDefinition{
		Declaration: FunctionDeclaration{
			Name:        "restart_container",
			Description: "Restart a specific Docker container",
			Parameters: FunctionParameter{
				Type: "OBJECT",
				Properties: map[string]FunctionParameter{
					"name": {
						Type:        "STRING",
						Description: "The name or ID of the Docker container to restart",
					},
				},
				Required: []string{"name"},
			},
		},
		IsStateful: true,
		Execute: func(ctx context.Context, args map[string]interface{}) (string, error) {
			name, ok := args["name"].(string)
			if !ok || name == "" {
				return "", fmt.Errorf("missing or invalid 'name' argument")
			}
			return runCmd("docker", "restart", name)
		},
	}

	// 6. stop_container
	r.tools["stop_container"] = ToolDefinition{
		Declaration: FunctionDeclaration{
			Name:        "stop_container",
			Description: "Stop a specific Docker container",
			Parameters: FunctionParameter{
				Type: "OBJECT",
				Properties: map[string]FunctionParameter{
					"name": {
						Type:        "STRING",
						Description: "The name or ID of the Docker container to stop",
					},
				},
				Required: []string{"name"},
			},
		},
		IsStateful: true,
		Execute: func(ctx context.Context, args map[string]interface{}) (string, error) {
			name, ok := args["name"].(string)
			if !ok || name == "" {
				return "", fmt.Errorf("missing or invalid 'name' argument")
			}
			return runCmd("docker", "stop", name)
		},
	}

	// 7. search_past_logs
	r.tools["search_past_logs"] = ToolDefinition{
		Declaration: FunctionDeclaration{
			Name:        "search_past_logs",
			Description: "Search the append-only JSONL log file for past system alerts, metrics anomalies, or agent conversations containing a query term",
			Parameters: FunctionParameter{
				Type: "OBJECT",
				Properties: map[string]FunctionParameter{
					"query": {
						Type:        "STRING",
						Description: "The keyword or phrase to search for in historical logs",
					},
				},
				Required: []string{"query"},
			},
		},
		IsStateful: false,
		Execute: func(ctx context.Context, args map[string]interface{}) (string, error) {
			query, ok := args["query"].(string)
			if !ok || query == "" {
				return "", fmt.Errorf("missing or invalid 'query' argument")
			}

			logPath := os.Getenv("LOG_FILE_PATH")
			if logPath == "" {
				logPath = "nas-watchdog.jsonl"
			}

			file, err := os.Open(logPath)
			if err != nil {
				return "", fmt.Errorf("failed to open log file %s: %w", logPath, err)
			}
			defer file.Close()

			var matches []string
			scanner := bufio.NewScanner(file)
			for scanner.Scan() {
				line := scanner.Text()
				if strings.Contains(strings.ToLower(line), strings.ToLower(query)) {
					matches = append(matches, line)
				}
				if len(matches) >= 20 {
					break
				}
			}

			if len(matches) == 0 {
				return "No matching logs found.", nil
			}

			return strings.Join(matches, "\n"), nil
		},
	}

	// 8. schedule_task
	r.tools["schedule_task"] = ToolDefinition{
		Declaration: FunctionDeclaration{
			Name:        "schedule_task",
			Description: "Schedule a shell command to execute repeatedly at a regular interval (in minutes)",
			Parameters: FunctionParameter{
				Type: "OBJECT",
				Properties: map[string]FunctionParameter{
					"name": {
						Type:        "STRING",
						Description: "Unique name for the scheduled task",
					},
					"interval_minutes": {
						Type:        "INTEGER",
						Description: "Time interval in minutes between executions",
					},
					"command": {
						Type:        "STRING",
						Description: "The command to run periodically on the system",
					},
				},
				Required: []string{"name", "interval_minutes", "command"},
			},
		},
		IsStateful: true,
		Execute: func(ctx context.Context, args map[string]interface{}) (string, error) {
			name, ok := args["name"].(string)
			if !ok || name == "" {
				return "", fmt.Errorf("missing or invalid 'name'")
			}
			intervalVal, ok := args["interval_minutes"]
			if !ok {
				return "", fmt.Errorf("missing 'interval_minutes'")
			}
			var intervalMin int
			switch v := intervalVal.(type) {
			case float64:
				intervalMin = int(v)
			case int:
				intervalMin = v
			default:
				return "", fmt.Errorf("invalid 'interval_minutes' type")
			}
			if intervalMin <= 0 {
				return "", fmt.Errorf("interval_minutes must be positive")
			}
			command, ok := args["command"].(string)
			if !ok || command == "" {
				return "", fmt.Errorf("missing or invalid 'command'")
			}

			r.schedulerMutex.Lock()
			defer r.schedulerMutex.Unlock()

			if _, exists := r.schedulerTasks[name]; exists {
				return "", fmt.Errorf("a task with the name '%s' already exists", name)
			}

			taskCtx, cancel := context.WithCancel(context.Background())
			task := &ScheduledTask{
				Name:     name,
				Interval: time.Duration(intervalMin) * time.Minute,
				Command:  command,
				Cancel:   cancel,
			}
			r.schedulerTasks[name] = task

			// Start the scheduler thread
			go func() {
				ticker := time.NewTicker(task.Interval)
				defer ticker.Stop()
				for {
					select {
					case <-ticker.C:
						log.Printf("[Scheduler] Executing scheduled command '%s': %s", task.Name, task.Command)
						out, err := runCmd("sh", "-c", task.Command)
						status := "SUCCESS ✅"
						if err != nil {
							status = fmt.Sprintf("FAILED ❌ (%v)", err)
						}
						// Direct message updates to the Telegram user
						report := fmt.Sprintf("🔔 *Scheduled Task executed: %s*\nStatus: %s\n\n*Output:*\n```\n%s\n```", task.Name, status, out)
						_, _ = r.tgClient.SendMessage(context.Background(), r.chatID, report)
					case <-taskCtx.Done():
						log.Printf("[Scheduler] Task '%s' has been cancelled.", task.Name)
						return
					}
				}
			}()

			return fmt.Sprintf("Successfully scheduled task '%s' to run every %d minutes.", name, intervalMin), nil
		},
	}

	// 9. list_scheduled_tasks
	r.tools["list_scheduled_tasks"] = ToolDefinition{
		Declaration: FunctionDeclaration{
			Name:        "list_scheduled_tasks",
			Description: "List all currently scheduled periodic commands",
			Parameters: FunctionParameter{
				Type:       "OBJECT",
				Properties: map[string]FunctionParameter{},
			},
		},
		IsStateful: false,
		Execute: func(ctx context.Context, args map[string]interface{}) (string, error) {
			r.schedulerMutex.Lock()
			defer r.schedulerMutex.Unlock()

			if len(r.schedulerTasks) == 0 {
				return "No scheduled tasks are running.", nil
			}

			var lines []string
			for _, t := range r.schedulerTasks {
				lines = append(lines, fmt.Sprintf("- Name: `%s` | Interval: every %s | Command: `%s`", t.Name, t.Interval, t.Command))
			}
			return strings.Join(lines, "\n"), nil
		},
	}

	// 10. unschedule_task
	r.tools["unschedule_task"] = ToolDefinition{
		Declaration: FunctionDeclaration{
			Name:        "unschedule_task",
			Description: "Cancel and remove a scheduled task by name",
			Parameters: FunctionParameter{
				Type: "OBJECT",
				Properties: map[string]FunctionParameter{
					"name": {
						Type:        "STRING",
						Description: "The unique name of the scheduled task to cancel",
					},
				},
				Required: []string{"name"},
			},
		},
		IsStateful: true,
		Execute: func(ctx context.Context, args map[string]interface{}) (string, error) {
			name, ok := args["name"].(string)
			if !ok || name == "" {
				return "", fmt.Errorf("missing or invalid 'name'")
			}

			r.schedulerMutex.Lock()
			defer r.schedulerMutex.Unlock()

			task, exists := r.schedulerTasks[name]
			if !exists {
				return fmt.Sprintf("Task '%s' not found.", name), nil
			}

			task.Cancel()
			delete(r.schedulerTasks, name)
			return fmt.Sprintf("Successfully cancelled scheduled task '%s'.", name), nil
		},
	}

	// 11. web_scrape
	r.tools["web_scrape"] = ToolDefinition{
		Declaration: FunctionDeclaration{
			Name:        "web_scrape",
			Description: "Scrape text content from a web page or online documentation URL",
			Parameters: FunctionParameter{
				Type: "OBJECT",
				Properties: map[string]FunctionParameter{
					"url": {
						Type:        "STRING",
						Description: "The absolute HTTP/HTTPS URL of the page to scrape",
					},
				},
				Required: []string{"url"},
			},
		},
		IsStateful: false,
		Execute: func(ctx context.Context, args map[string]interface{}) (string, error) {
			targetURL, ok := args["url"].(string)
			if !ok || targetURL == "" {
				return "", fmt.Errorf("missing or invalid 'url' argument")
			}
			return WebScrape(targetURL, time.Duration(r.cfg.WebScraperTimeoutSeconds)*time.Second)
		},
	}

	// 12. web_crawl
	r.tools["web_crawl"] = ToolDefinition{
		Declaration: FunctionDeclaration{
			Name:        "web_crawl",
			Description: "Discover same-domain links on a web page to explore documentation structure",
			Parameters: FunctionParameter{
				Type: "OBJECT",
				Properties: map[string]FunctionParameter{
					"url": {
						Type:        "STRING",
						Description: "The absolute HTTP/HTTPS URL to crawl",
					},
				},
				Required: []string{"url"},
			},
		},
		IsStateful: false,
		Execute: func(ctx context.Context, args map[string]interface{}) (string, error) {
			targetURL, ok := args["url"].(string)
			if !ok || targetURL == "" {
				return "", fmt.Errorf("missing or invalid 'url' argument")
			}
			links, err := WebCrawl(targetURL, time.Duration(r.cfg.WebScraperTimeoutSeconds)*time.Second)
			if err != nil {
				return "", err
			}
			if len(links) == 0 {
				return "No same-domain links discovered.", nil
			}
			return strings.Join(links, "\n"), nil
		},
	}

	// 13. web_search
	r.tools["web_search"] = ToolDefinition{
		Declaration: FunctionDeclaration{
			Name:        "web_search",
			Description: "Search the web using DuckDuckGo (no API key required) to find documentation, instructions, or troubleshoot problems",
			Parameters: FunctionParameter{
				Type: "OBJECT",
				Properties: map[string]FunctionParameter{
					"query": {
						Type:        "STRING",
						Description: "The search query to look up on the web",
					},
				},
				Required: []string{"query"},
			},
		},
		IsStateful: false,
		Execute: func(ctx context.Context, args map[string]interface{}) (string, error) {
			query, ok := args["query"].(string)
			if !ok || query == "" {
				return "", fmt.Errorf("missing or invalid 'query' argument")
			}
			return SearchDuckDuckGo(query, time.Duration(r.cfg.WebScraperTimeoutSeconds)*time.Second, r.cfg.MaxSearchResults)
		},
	}
}

func runCmd(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("command execution failed: %w (output: %s)", err, string(output))
	}
	return string(output), nil
}

func generateActionID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Register allows registering custom tools dynamically after Registry initialization.
func (r *Registry) Register(name string, def ToolDefinition) {
	r.mutex.Lock()
	defer r.mutex.Unlock()
	r.tools[name] = def
}

