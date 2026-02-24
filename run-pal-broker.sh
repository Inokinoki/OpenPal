#!/bin/bash
# run-pal-broker.sh - Helper script to run pal-broker with AI CLI tools

# Load Claude API configuration
export ANTHROPIC_BASE_URL="https://coding.dashscope.aliyuncs.com/apps/anthropic"
export ANTHROPIC_MODEL="qwen3.5-plus"
export ANTHROPIC_AUTH_TOKEN="sk-sp-f4d777a3699b41b3a5024d48728b9810"

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
