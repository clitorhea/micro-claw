package config

import (
	"os"
	"strconv"
)

type Config struct {
	TelegramToken              string
	TelegramUserID             int64
	LLMProvider                string // "gemini" or "deepseek"
	LLMAPIKey                  string
	LLMModel                   string
	LLMAPIURL                  string
	CheckIntervalMinutes       int
	CPUThreshold               float64
	MemoryThreshold            float64
	DiskThreshold              float64
	LogFilePath                string
	UserApprovalTimeoutMinutes int
	LLMTimeoutSeconds          int
	WebScraperTimeoutSeconds   int
	MaxSearchResults           int
	DiskMountPath              string
	TopProcessesLimit          int
}

func LoadConfig() (*Config, error) {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	userIDStr := os.Getenv("TELEGRAM_USER_ID")

	var userID int64
	if userIDStr != "" {
		val, err := strconv.ParseInt(userIDStr, 10, 64)
		if err != nil {
			return nil, err
		}
		userID = val
	}

	provider := os.Getenv("LLM_PROVIDER")
	if provider == "" {
		provider = "gemini"
	}

	apiKey := os.Getenv("LLM_API_KEY")
	if apiKey == "" {
		if provider == "gemini" {
			apiKey = os.Getenv("GEMINI_API_KEY")
		} else {
			apiKey = os.Getenv("DEEPSEEK_API_KEY")
		}
	}

	model := os.Getenv("LLM_MODEL")
	if model == "" {
		if provider == "gemini" {
			model = os.Getenv("GEMINI_MODEL")
			if model == "" {
				model = "gemini-1.5-flash"
			}
		} else {
			model = os.Getenv("DEEPSEEK_MODEL")
			if model == "" {
				model = "deepseek-v4-flash"
			}
		}
	}

	apiURL := os.Getenv("LLM_API_URL")
	if apiURL == "" {
		if provider == "gemini" {
			apiURL = os.Getenv("GEMINI_API_URL")
			if apiURL == "" {
				apiURL = "https://generativelanguage.googleapis.com/v1beta/models/"
			}
		} else {
			apiURL = os.Getenv("DEEPSEEK_API_URL")
			if apiURL == "" {
				apiURL = "https://api.deepseek.com/v1"
			}
		}
	}

	intervalStr := os.Getenv("CHECK_INTERVAL_MINUTES")
	interval := 5
	if intervalStr != "" {
		if val, err := strconv.Atoi(intervalStr); err == nil {
			interval = val
		}
	}

	cpuStr := os.Getenv("CPU_THRESHOLD_PERCENT")
	cpuThresh := 80.0
	if cpuStr != "" {
		if val, err := strconv.ParseFloat(cpuStr, 64); err == nil {
			cpuThresh = val
		}
	}

	memStr := os.Getenv("MEMORY_THRESHOLD_PERCENT")
	memThresh := 80.0
	if memStr != "" {
		if val, err := strconv.ParseFloat(memStr, 64); err == nil {
			memThresh = val
		}
	}

	diskStr := os.Getenv("DISK_THRESHOLD_PERCENT")
	diskThresh := 85.0
	if diskStr != "" {
		if val, err := strconv.ParseFloat(diskStr, 64); err == nil {
			diskThresh = val
		}
	}

	logPath := os.Getenv("LOG_FILE_PATH")
	if logPath == "" {
		logPath = "nas-watchdog.jsonl"
	}

	userApprovalTimeoutStr := os.Getenv("USER_APPROVAL_TIMEOUT_MINUTES")
	userApprovalTimeout := 10
	if userApprovalTimeoutStr != "" {
		if val, err := strconv.Atoi(userApprovalTimeoutStr); err == nil {
			userApprovalTimeout = val
		}
	}

	llmTimeoutStr := os.Getenv("LLM_TIMEOUT_SECONDS")
	llmTimeout := 60
	if llmTimeoutStr != "" {
		if val, err := strconv.Atoi(llmTimeoutStr); err == nil {
			llmTimeout = val
		}
	}

	webScraperTimeoutStr := os.Getenv("WEB_SCRAPER_TIMEOUT_SECONDS")
	webScraperTimeout := 15
	if webScraperTimeoutStr != "" {
		if val, err := strconv.Atoi(webScraperTimeoutStr); err == nil {
			webScraperTimeout = val
		}
	}

	maxSearchResultsStr := os.Getenv("MAX_SEARCH_RESULTS")
	maxSearchResults := 6
	if maxSearchResultsStr != "" {
		if val, err := strconv.Atoi(maxSearchResultsStr); err == nil {
			maxSearchResults = val
		}
	}

	diskMountPath := os.Getenv("DISK_MOUNT_PATH")
	if diskMountPath == "" {
		diskMountPath = "/host"
	}

	topProcessesLimitStr := os.Getenv("TOP_PROCESSES_LIMIT")
	topProcessesLimit := 15
	if topProcessesLimitStr != "" {
		if val, err := strconv.Atoi(topProcessesLimitStr); err == nil {
			topProcessesLimit = val
		}
	}

	return &Config{
		TelegramToken:              token,
		TelegramUserID:             userID,
		LLMProvider:                provider,
		LLMAPIKey:                  apiKey,
		LLMModel:                   model,
		LLMAPIURL:                  apiURL,
		CheckIntervalMinutes:       interval,
		CPUThreshold:               cpuThresh,
		MemoryThreshold:            memThresh,
		DiskThreshold:              diskThresh,
		LogFilePath:                logPath,
		UserApprovalTimeoutMinutes: userApprovalTimeout,
		LLMTimeoutSeconds:          llmTimeout,
		WebScraperTimeoutSeconds:   webScraperTimeout,
		MaxSearchResults:           maxSearchResults,
		DiskMountPath:              diskMountPath,
		TopProcessesLimit:          topProcessesLimit,
	}, nil
}
