#!/bin/bash
# run-pal-broker.sh - Helper script to run pal-broker with Claude Code

# Load Claude API configuration
export ANTHROPIC_BASE_URL="https://coding.dashscope.aliyuncs.com/apps/anthropic"
export ANTHROPIC_MODEL="qwen3.5-plus"
export ANTHROPIC_AUTH_TOKEN="sk-sp-f4d777a3699b41b3a5024d48728b9810"

# Build if needed
if [ ! -f "./pal-broker" ]; then
    echo "Building pal-broker..."
    make build
fi

# Run pal-broker with arguments
./pal-broker "$@"
