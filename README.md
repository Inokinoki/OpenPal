# PalBroker

WebSocket broker for AI CLI tools with session management and real-time streaming.

## Features

- Multi-provider support (Claude, Codex, Copilot, Gemini, OpenCode)
- WebSocket API for real-time communication
- Session persistence and resume
- Concurrent client handling
- JSON streaming output

## Installation

```bash
# Clone the repository
git clone <repository-url>
cd PalBroker

# Build
make build

# Or install to /usr/local/bin
make install
```

## Usage

```bash
# Basic usage
./pal-broker --quest-id my-task --provider claude --work-dir /path/to/project

# With specific port
./pal-broker --quest-id my-task --provider claude --port :8080

# Show version
./pal-broker --version
```

## Environment Variables

- `PAL_PROVIDER` - Default AI provider (claude, codex, copilot, gemini, opencode)
- `PAL_WORK_DIR` - Working directory for AI CLI
- `PAL_<PROVIDER>_PATH` - Custom path to CLI executable
- `PAL_CAPABILITIES` - Comma-separated list of capabilities
- `ANTHROPIC_API_KEY` - API key for Claude
- `OPENAI_API_KEY` - API key for Codex
- `GITHUB_TOKEN` - Token for Copilot

## WebSocket API

Connect to `ws://localhost:<port>/ws?device=<device-id>` to stream AI responses.

## Development

```bash
# Run tests
make test

# Run tests with coverage
make coverage

# Format code
make fmt

# Run linter
make lint

# Build for all platforms
make build-all
```

## Configuration

Create a `.env` file in your working directory:

```
ANTHROPIC_API_KEY=your-key-here
PAL_PROVIDER=claude
```

## License

See [LICENSE](LICENSE) file.
