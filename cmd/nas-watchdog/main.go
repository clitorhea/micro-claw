package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"micro-claw/internal/agent"
	"micro-claw/internal/config"
	"micro-claw/internal/metrics"
	"micro-claw/internal/telegram"
	"micro-claw/internal/tools"
)

func main() {
	log.Println("[Watchdog] Initializing MicroClaw NAS Watchdog...")

	// 1. Load configuration
	cfg, err := config.LoadConfig()
	if err != nil {
		log.Fatalf("[Watchdog] Failed to load configuration: %v", err)
	}

	if cfg.TelegramToken == "" || cfg.TelegramUserID == 0 || cfg.LLMAPIKey == "" {
		log.Fatalf("[Watchdog] Missing mandatory environment variables: TELEGRAM_BOT_TOKEN, TELEGRAM_USER_ID, and LLM_API_KEY (or provider specific key) must be set.")
	}

	// 2. Initialize Telegram Client
	tgClient := telegram.NewClient(cfg.TelegramToken, cfg.TelegramUserID)

	// 3. Initialize Tool Registry
	registry := tools.NewRegistry(tgClient, cfg.TelegramUserID)

	// 4. Initialize LLM Agent Router
	agentRouter := agent.NewAgent(cfg, tgClient, registry)

	// Create root cancellation context
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle OS Signals for graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigChan
		log.Printf("[Watchdog] Received signal %v, initiating shutdown...", sig)
		cancel()
	}()

	// 5. Start Telegram Polling (Ingress 1)
	msgChan := make(chan *telegram.Message, 100)
	callbackChan := make(chan *telegram.CallbackQuery, 100)
	go tgClient.StartPolling(ctx, msgChan, callbackChan)

	// 6. Start Ticker/Cron Loop (Ingress 2)
	go runHealthCheckLoop(ctx, cfg, agentRouter)

	// Startup notification
	startupMsg := "🚀 *MicroClaw NAS Watchdog Started*\n\nMonitoring CPU, Memory, Disk space and Docker containers. Standing by for instructions."
	_, err = tgClient.SendMessage(ctx, cfg.TelegramUserID, startupMsg)
	if err != nil {
		log.Printf("[Watchdog] Failed to send startup message: %v", err)
	}

	log.Println("[Watchdog] Daemon successfully started, processing loops...")

	// 7. Message Coordination Loop
	for {
		select {
		case msg := <-msgChan:
			// Parse Telegram commands starting with '/'
			if strings.HasPrefix(msg.Text, "/") {
				go handleTelegramCommand(ctx, msg.Text, tgClient, agentRouter)
			} else {
				// Process normal query in a separate goroutine so it doesn't block callbacks
				go agentRouter.HandleUserMessage(ctx, msg.Text)
			}

		case cb := <-callbackChan:
			// Handle button presses
			registry.HandleCallback(ctx, cb)

		case <-ctx.Done():
			shutdownMsg := "🛑 *MicroClaw NAS Watchdog Shutting Down*"
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, _ = tgClient.SendMessage(shutdownCtx, cfg.TelegramUserID, shutdownMsg)
			shutdownCancel()
			registry.UnscheduleAll()
			log.Println("[Watchdog] Exited coordinate loop, goodbye.")
			return
		}
	}
}

func runHealthCheckLoop(ctx context.Context, cfg *config.Config, router *agent.Agent) {
	ticker := time.NewTicker(time.Duration(cfg.CheckIntervalMinutes) * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			log.Println("[Watchdog] Executing health check...")
			cpu, mem, disk, err := metrics.GetSystemMetrics()
			if err != nil {
				log.Printf("[Watchdog] Error reading system metrics: %v", err)
				continue
			}

			log.Printf("[Watchdog] System Metrics: CPU: %.2f%%, Mem: %.2f%%, Disk: %.2f%%", cpu, mem, disk)

			var anomalies []string
			if cpu > cfg.CPUThreshold {
				anomalies = append(anomalies, fmt.Sprintf("- CPU usage is %.2f%% (threshold: %.2f%%)", cpu, cfg.CPUThreshold))
			}
			if mem > cfg.MemoryThreshold {
				anomalies = append(anomalies, fmt.Sprintf("- Memory usage is %.2f%% (threshold: %.2f%%)", mem, cfg.MemoryThreshold))
			}
			if disk > cfg.DiskThreshold {
				anomalies = append(anomalies, fmt.Sprintf("- Disk usage is %.2f%% (threshold: %.2f%%)", disk, cfg.DiskThreshold))
			}

			if len(anomalies) > 0 {
				topProc, _ := metrics.GetTopProcesses()
				payload := fmt.Sprintf("System Thresholds Breached:\n%s\n\n*Current Top Processes (host):*\n```\n%s\n```",
					strings.Join(anomalies, "\n"), topProc)
				go router.HandleAnomaly(ctx, payload)
			}
		}
	}
}

func handleTelegramCommand(ctx context.Context, text string, tgClient *telegram.Client, router *agent.Agent) {
	parts := strings.Fields(text)
	if len(parts) == 0 {
		return
	}
	cmd := parts[0]
	chatID := router.Config().TelegramUserID

	switch cmd {
	case "/help":
		helpText := "🛠️ *MicroClaw Assistant Commands*\n\n" +
			"`/stats` - Instantly view system metrics (CPU, memory, disk)\n" +
			"`/status` - List all active/inactive containers\n" +
			"`/reset` - Clear current LLM conversation memory\n" +
			"`/help` - Show this instructions menu"
		_, _ = tgClient.SendMessage(ctx, chatID, helpText)

	case "/reset":
		router.ClearHistory()
		_, _ = tgClient.SendMessage(ctx, chatID, "🧹 *Conversation history cleared.*")

	case "/stats":
		cpu, mem, disk, err := metrics.GetSystemMetrics()
		if err != nil {
			_, _ = tgClient.SendMessage(ctx, chatID, fmt.Sprintf("❌ *Failed to fetch metrics:* %v", err))
			return
		}
		topProc, _ := metrics.GetTopProcesses()
		report := fmt.Sprintf("📊 *System Resource Metrics*\n\n"+
			"*CPU:* %.2f%%\n"+
			"*Memory:* %.2f%%\n"+
			"*Disk Space:* %.2f%%\n\n"+
			"*Top Processes (host):*\n```\n%s\n```", cpu, mem, disk, topProc)
		_, _ = tgClient.SendMessage(ctx, chatID, report)

	case "/status":
		cmdExec := exec.Command("docker", "ps", "-a", "--format", "table {{.Names}}\t{{.Status}}\t{{.Image}}")
		output, err := cmdExec.CombinedOutput()
		if err != nil {
			_, _ = tgClient.SendMessage(ctx, chatID, fmt.Sprintf("❌ *Failed to retrieve container status:* %v\n\nOutput: %s", err, string(output)))
			return
		}
		_, _ = tgClient.SendMessage(ctx, chatID, fmt.Sprintf("🐳 *Docker Container List*\n\n```\n%s\n```", string(output)))

	default:
		_, _ = tgClient.SendMessage(ctx, chatID, fmt.Sprintf("❓ *Unknown command:* %s. Type `/help` for available options.", cmd))
	}
}
