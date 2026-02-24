#!/bin/bash
# run-pal-broker.sh - Helper script to run pal-broker with AI CLI tools

# Load Claude API configuration
export ANTHROPIC_BASE_URL="https://coding.dashscope.aliyuncs.com/apps/anthropic"
export ANTHROPIC_MODEL="qwen3.5-plus"
# Load from environment or secrets file
# export ANTHROPIC_AUTH_TOKEN="your-token-here"

# Load GitHub Copilot token (if available)
if [ -f "~/.pal-broker-secrets" ]; then
    source ~/.pal-broker-secrets
fi

# Build if needed
if [ ! -f "./pal-broker" ]; then
    echo "Building pal-broker..."
    make build
fi

# Run pal-broker with arguments
./pal-broker "$@"
