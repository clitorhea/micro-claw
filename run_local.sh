#!/bin/bash

# Load environment variables from .env if it exists
if [ -f .env ]; then
    echo "🔑 Loading environment variables from .env..."
    while IFS= read -r line || [ -n "$line" ]; do
        # Strip potential Windows carriage returns
        line=$(echo "$line" | tr -d '\r')
        # Ignore comments and empty lines
        if [[ ! "$line" =~ ^# && ! -z "$line" ]]; then
            # Evaluate to handle potential wrapping quotes
            eval "export $line"
        fi
    done < .env
else
    echo "❌ Error: .env file not found!"
    echo "Please copy .env.example to .env and configure your credentials:"
    echo "  cp .env.example .env"
    exit 1
fi

# Set default local testing overrides (unless already specified in .env)
export CHECK_INTERVAL_MINUTES="${CHECK_INTERVAL_MINUTES:-1}"
export DISK_MOUNT_PATH="${DISK_MOUNT_PATH:-/}"
export LOG_FILE_PATH="${LOG_FILE_PATH:-nas-watchdog-local.jsonl}"

# Build and run the daemon
echo "🚀 Starting MicroClaw NAS Watchdog daemon locally..."
go run cmd/nas-watchdog/main.go
