#!/bin/bash
# Check which CLI tools are installed in the Docker image

set -e

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo "========================================="
echo "PalBroker CLI Tools Availability Check"
echo "========================================="
echo ""

# Function to check CLI
check_cli() {
    local cli_name=$1
    local check_command=$2
    local description=$3

    echo -n "Checking $cli_name... "

    if eval "$check_command" &> /dev/null; then
        echo -e "${GREEN}✓ INSTALLED${NC}"
        version=$(eval "$check_command --version 2>&1 | head -1" || echo "Version unknown")
        echo "  └─ $version"
        return 0
    else
        echo -e "${RED}✗ NOT FOUND${NC}"
        echo "  └─ $description"
        return 1
    fi
}

# Check each CLI
echo "AI CLI Tools:"
echo "-------------"

check_cli "Claude Code" "command -v claude" \
    "Install from: https://claude.ai/install.sh"

check_cli "Codex CLI" "command -v codex" \
    "Install from: https://github.com/openai/codex-cli"

check_cli "Gemini CLI" "command -v gemini" \
    "Install: npm install -g @google/generative-ai-cli"

check_cli "GitHub Copilot" "command -v copilot" \
    "Install: npm install -g @github/copilot-cli"

check_cli "OpenCode" "command -v opencode" \
    "Install from: https://github.com/opencode/opencode"

echo ""
echo "Development Tools:"
echo "------------------"

check_cli "Go" "command -v go" \
    "Install: https://golang.org/dl/"

check_cli "Node.js" "command -v node" \
    "Install: https://nodejs.org/"

check_cli "Python" "command -v python3" \
    "Install: https://python.org/"

check_cli "Docker" "command -v docker" \
    "Install: https://docs.docker.com/get-docker/"

echo ""
echo "========================================="
echo "Summary:"
echo "========================================="

# Count installed tools
total=9
installed=0

command -v claude &> /dev/null && ((installed++))
command -v codex &> /dev/null && ((installed++))
command -v gemini &> /dev/null && ((installed++))
command -v copilot &> /dev/null && ((installed++))
command -v opencode &> /dev/null && ((installed++))
command -v go &> /dev/null && ((installed++))
command -v node &> /dev/null && ((installed++))
command -v python3 &> /dev/null && ((installed++))
command -v docker &> /dev/null && ((installed++))

echo "Installed: $installed/$total tools"

if [ $installed -eq $total ]; then
    echo -e "${GREEN}All tools installed! Ready for testing.${NC}"
    exit 0
elif [ $installed -ge 5 ]; then
    echo -e "${YELLOW}Some tools missing. Testing may be limited.${NC}"
    exit 0
else
    echo -e "${RED}Many tools missing. Please install required tools.${NC}"
    exit 1
fi
