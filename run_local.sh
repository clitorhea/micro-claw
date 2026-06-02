#!/bin/bash
# MicroClaw local development/testing run script

# 1. Export mandatory Telegram bot credentials
export TELEGRAM_BOT_TOKEN="YOUR_TELEGRAM_BOT_TOKEN_HERE"
export TELEGRAM_USER_ID="YOUR_TELEGRAM_USER_ID_HERE" # Authorized user ID (integer)

# 2. Export LLM settings (Select "gemini" or "deepseek")
export LLM_PROVIDER="deepseek"
export LLM_API_KEY="YOUR_LLM_API_KEY_HERE"
export LLM_MODEL="deepseek-v4-flash"

# 3. Export monitoring variables (reduced interval to 1 minute for faster testing iterations)
export CHECK_INTERVAL_MINUTES="1"
export CPU_THRESHOLD_PERCENT="80.0"
export MEMORY_THRESHOLD_PERCENT="80.0"
export DISK_THRESHOLD_PERCENT="85.0"
export LOG_FILE_PATH="nas-watchdog-local.jsonl"

echo "============================================="
echo "Starting MicroClaw Watchdog Daemon locally..."
echo "Configured Provider: $LLM_PROVIDER ($LLM_MODEL)"
echo "Target User ID:      $TELEGRAM_USER_ID"
echo "============================================="

# 4. Run the Go application directly
go run cmd/nas-watchdog/main.go
