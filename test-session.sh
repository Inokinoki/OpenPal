#!/bin/bash
# test-session.sh - Test Claude session resume functionality

set -e

echo "=== Testing Claude Session Resume ==="

# Clean up
pkill -f pal-broker 2>/dev/null || true
rm -rf /tmp/pal-broker/test-* 2>/dev/null || true

# Start pal-broker
echo "Starting pal-broker..."
export ANTHROPIC_BASE_URL="https://coding.dashscope.aliyuncs.com/apps/anthropic"
export ANTHROPIC_MODEL="qwen3.5-plus"
export ANTHROPIC_AUTH_TOKEN="sk-sp-f4d777a3699b41b3a5024d48728b9810"

./pal-broker --quest-id "test-session" --provider "claude" --work-dir "." &
PID=$!
sleep 5

PORT=$(cat /tmp/pal-broker/test-session/ws_port)
echo "PalBroker started on port $PORT"

# Send first task
echo "Sending first task..."
./ws-test -url "ws://localhost:$PORT/ws?device=d1" -task "Say hello" &
sleep 30

# Check session file
echo "=== After first task ==="
ls -la /tmp/pal-broker/test-session/
if [ -f /tmp/pal-broker/test-session/.claude-session.json ]; then
    echo "✅ Session file created!"
    cat /tmp/pal-broker/test-session/.claude-session.json
else
    echo "❌ No session file"
fi

# Check output
if [ -f /tmp/pal-broker/test-session/output.jsonl ]; then
    echo "=== Output (first 5 lines) ==="
    head -5 /tmp/pal-broker/test-session/output.jsonl
else
    echo "❌ No output file"
fi

# Send second task (should resume)
echo "Sending second task (should resume)..."
./ws-test -url "ws://localhost:$PORT/ws?device=d1" -task "What did you say before?" &
sleep 30

echo "=== After second task ==="
if [ -f /tmp/pal-broker/test-session/.claude-session.json ]; then
    echo "Session ID:"
    cat /tmp/pal-broker/test-session/.claude-session.json | grep session_id
else
    echo "❌ No session file"
fi

# Cleanup
pkill -f pal-broker
echo "=== Test complete ==="
