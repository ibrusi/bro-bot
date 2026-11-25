# 🤖 bro-bot (Bro Bot)

[![Go Version](https://img.shields.io/badge/Go-1.23+-00ADD8?style=flat&logo=go)](https://golang.org/)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Platform](https://img.shields.io/badge/platform-Linux-lightgrey.svg)](https://kernel.org)

**English** | [Русский](README.ru.md)

**bro-bot** is an autonomous Go-based service bridge connecting **Telegram** with agentic developer environments: **Google Antigravity CLI (`agy`)**, **Claude Code CLI (`claude`)**, and **Grok CLI (`grok`)**.

The bot empowers a developer or engineering team to manage a pool of projects, assign coding and refactoring tasks, approve interactive architectural plans, answer clarifying agent questions, inspect session transcripts, track token spending, and monitor server resource usage — all without leaving Telegram.

<a id="disclaimer"></a>
> [!WARNING]
> **Safety and Usage Disclaimer**:
> - **Autonomous Command Execution & Elevated Privileges**: `bro-bot` executes AI agent CLIs (`agy`, `claude`) with elevated system permissions (`--dangerously-skip-permissions`). Grok tasks use `grok agent --always-approve stdio` (disable with `GROK_ACP_YOLO=false`). Autonomous agents can run shell commands, create, modify, or delete files, install dependencies, and push Git changes without manual approval for every action. Always run the bot under a dedicated non-root user account (e.g., `deploy`), restrict `sudoers` privileges, and strictly limit bot access via `TELEGRAM_ADMIN_ID` (never run the bot as `root`).
> - **Account Suspension Risk with Subscription Plans (MCP Mode)**: Running CLI agents authenticated with personal subscriptions (Claude Pro/Team/Max, Google One AI Premium/Gemini Advanced) in automated bot workflows (`mcp` mode) violates the Terms of Service of those providers (prohibition against automated or unattended use of non-API interfaces). Using `mcp` mode is strictly at your own risk and may lead to account penalties or bans. In contrast, operating via official API keys (`api` mode) fully complies with provider policies and will not result in account bans.
> - **Verification Responsibility**: While agents follow `AGENTS.md` guidelines by running tests and linters, final validation of all generated code, commits, and Pull Requests before merging them into production remains the sole responsibility of the repository maintainer.
> - **Token Quotas & API Costs**: Autonomous agents make numerous calls to advanced LLMs (Google Gemini, Anthropic Claude), consuming significant context, generation, and reasoning (thinking) tokens. Regularly inspect your quotas, credit balances, and provider dashboards, as well as bot commands `/usage` and `/tokens`, to prevent unexpected expenses.
> - **Disclaimer of Warranty (AS IS)**: This open-source software is distributed under the MIT License on an "AS IS" basis, without warranty of any kind, express or implied. The authors and contributors accept no liability for data loss, system disruption, account suspensions, or financial expenses resulting from its operation.


---

## 📑 Table of Contents

- [✨ Key Features](#-key-features)
- [🏗 Architecture](#-architecture)
- [📦 System Requirements](#-system-requirements)
- [🚀 Installation & Build](#-installation--build)
- [⚙️ Configuration (.env)](#️-configuration-env)
- [🛠 Systemd Setup & Autostart](#-systemd-setup--autostart)
- [💬 Bot Command Reference](#-bot-command-reference)
- [🌍 Interface Language & Localization](#-interface-language--localization)
- [📋 Agent Guidelines (AGENTS.md)](#-agent-guidelines-agentmd)
- [🧪 Development & Testing](#-development--testing)
- [🔒 Security](#-security)
- [📄 License](#-license)

---

## ✨ Key Features

- **Project and Repository Management**:
  - Instant listing and switching between active project workspaces (`/project [name]`).
  - On-the-fly Git cloning over SSH or HTTPS (`/clone <url> [name]`) with automatic provisioning of the `AGENTS.md` rules template.
- **Conversational Mode (default)**:
  - A plain message is a conversation with the agent about the active project: context is preserved between messages, so `/resume` is no longer needed.
  - When a message looks like a code-change request, the bot does not run it silently — it offers buttons: answer in chat, draft a plan, or create a task.
  - Works for every agent (`agy`, `claude`, `grok`) and every execution mode that agent supports (`acp`, `mcp`, `api`); switching `/agent` or `/mode` mid-conversation keeps the thread, and ACP agents (`agy`, `grok`) keep their own ACP sessions.
  - In chat the agent only reads the code and answers: file edits, branches, commits and PRs go through a task.
  - Controls: `/chat` (status), `/chat <question>`, `/chat new`, `/chat stop`, `/chatmode [on|off]`.
- **Intelligent Task Pipeline**:
  - Independent, per-project task queues.
  - Pre-planning mode (`/plan <task>`, `/planmode [on|off]`): the agent inspects the repository, composes and justifies an architectural plan, awaits user confirmation via interactive inline buttons (`/approve`, `/confirm`), and only then starts writing code.
  - Pause and resume (`/pause [id]`, `/resume [id] [answer]`).
  - Live execution streaming, recent logs, compact system resources (Bot, `agy`, `claude`, `grok`, `whisper-server`), and step tracking (`/status [id]`, `/tasks`).
  - Dialogue transcript inspection (`/history [id]`): view the full conversation and agent actions directly from the session logs.
  - Safe cancellation of stuck or outdated tasks (`/cancel [id]`).
- **Interactive Dialogues & Clarifying Questions (`ask_question` / `ask_user`)**:
  - Supports both single questions and multi-question batches (`questions[]`), multiple sequential Q&A rounds in `/new`, `/plan`, and `/chat`, and optional multi-select toggle buttons alongside single-choice buttons and single-message text replies.
  - Answer choices (as well as plan implementation variants) are always shown as a complete numbered list in the message text without truncation, while inline buttons adapt automatically: full labels for short options and balanced numeric buttons (`1`, `2`, …) with synchronized multi-select checkboxes and selection counters for longer options.
  - Flexible reply options: plain text messages (automatically routed to a waiting task even in another project), Telegram Reply to task notifications, or `/add` / `/resume` commands.
  - Configurable timeout (`QUESTION_TIMEOUT`): if no response is received in time, the task automatically pauses to unblock the project queue for other tasks.
- **Dynamic Model Registry**:
  - Automatic discovery of available models from the CLI (`agy models`) with disk caching and fallback mechanisms.
  - Per-agent aliases, resolved for the active agent:
    - `agy`: `default`, `orchestrator`, `orchestrator-carbon`, `orchestrator-argon`, `neon`, `carbon`, `argon`, `hydrogen`, `flash`, `flash-high`, `flash-low`, `pro`, `pro-low`, `sonnet` (Claude Sonnet 5.5), `opus` (Claude Opus 5.5), `oss`. There is no Claude Haiku in the `agy` catalog.
    - `claude`: `default`, `sonnet` (Claude Sonnet 5.5), `opus` (Claude Opus 5.5), `haiku` (Claude Haiku 5.5), `sonnet-5`.
    - `grok`: `default`, `grok`, `4.7` (Grok 4.7), `fast` (Grok 4.7 Fast), `4.6`, `4.5`.
    - An alias of another agent is not applied: the bot suggests switching with `/agent`.
  - Instant model viewing and switching (`/model`, `/model <name|alias>`, `/model refresh`).
  - Reasoning effort control (`/effort [auto|low|medium|high|xhigh|max]`), shared by `agy`, `claude`, and `grok` and saved across restarts. Grok has no `max` step: the bot sends `xhigh`.
  - Multi-agent toggle (`/agent <agy|claude|grok>`). Choosing `agy` or `grok` switches the execution mode to `acp` (default for both). Choosing `claude` switches to `mcp` when `acp` is active.
- **Quota, Token & Resource Monitoring**:
  - `/usage` (or `/limits`) — live quota limits and credit balances (`agy /quota`, `agy /credits`).
  - `/tokens` (or `/stats`) — granular token statistics for the current/completed task: Input, Output, Thinking, Cache Read, and Cache Hit Rate.
  - `/context [id]` — visual breakdown of context window utilization (system prompts, conversation history, tool outputs).
  - `/top` (or `/ps`, `/resources`) — real-time server telemetry: CPU, RAM, free disk space, Load Average, systemd cgroup metrics, active `agy`/`claude`/`grok` processes, and the `whisper-server` speech recognition engine.
- **Execution Modes: ACP, MCP & API (`/mode [acp|mcp|api]`)**:
  - `acp` (default for `agy` and `grok`): `agy acp` and `grok agent stdio` communicate over the Agent Client Protocol (newline-delimited JSON-RPC over stdio), resume the ACP session (`session/resume`), and access all 8 bot tools (`telegram_send_message`, `send_file`, `ask_user`, `report_progress`, `get_task_context`, `list_tasks`, `list_projects`, `sqlite_query`) through an ephemeral in-process HTTP MCP server (`127.0.0.1:0` with single-use `Bearer` token), without spawning `bot mcp-serve` or using a Unix socket.
  - `mcp` (default for `claude`): `claude` runs with the bot's built-in stdio Model Context Protocol server (`bot mcp-serve`) via `--mcp-config`. Its tools reach the running bot over a local Unix-socket bridge using step tokens and process-tree matching, so calls always land in the right task regardless of active project focus.
  - `api`: connects directly to provider APIs (Google Gemini API for `agy` & Anthropic Messages API for `claude`) with built-in workspace tools (`read_file`, `write_file`, `edit_file`, `list_dir`, `grep_search`, `find_files`, `run_command`) and in-process bot tools (`telegram_send_message`, `send_file`, `ask_user`, `report_progress`, `get_task_context`, `list_tasks`, `list_projects`, `sqlite_query`). `grok` does not support this mode.
  - Safe sandboxing: chat mode (`/chat`) provides strictly read-only file access, while file writes and command executions are enabled for pipeline tasks. All paths are validated within the project directory.
- **Multilingual Interface**:
  - Every bot reply, button, command description and agent prompt comes from a message catalog — English and Russian ship out of the box.
  - English is the default until a language is picked; the choice is made with `/language` and survives restarts.
  - A new language is one JSON file in `internal/i18n/locales/` — no Go code changes (see [Interface Language & Localization](#-interface-language--localization)).
- **Voice Message Recognition (Whisper)**:
  - Voice notes and audio files sent in Telegram are transcribed automatically via `whisper-server`.
  - Audio is converted to 16kHz mono WAV via `ffmpeg` before transcription to ensure 100% compatibility with any Whisper server.
  - Quoting recognized text in chat (`🗣 «...»`) with immediate execution: recognized commands (`/status`, `/plan`, etc.) are executed directly, and conversational speech routes to chat or task creation.
  - In debug mode (`DEBUG=true`), performance metrics are appended to the quote: STT latency, audio duration, and RTF (Real-Time Factor).
  - Voice recognition defaults to English (`en`). To switch the language, e.g. to Russian, set `WHISPER_LANGUAGE=ru` in your `.env`.
  - Easy setup via `sudo make install-whisper`.
- **File & Photo Uploads**:
  - Documents, source files, and photos sent in Telegram are automatically downloaded (up to 20 MB) into the target project's `uploads/` directory (resolved from a Reply target, a project prefix or `/plan` / `/new` in the caption, a task waiting for input, or the active project).
  - Safe sanitization against Path Traversal and shell injection, preserving Unicode and Cyrillic filenames.
  - Byte-by-byte deduplication: re-uploading an identical file reuses the existing file without creating duplicates or redundant disk writes.
  - Contextual routing: attached to waiting/active tasks (or via Telegram Reply), passed to new tasks (`/new` / `/plan`), or explored in conversational mode (`/chat`).
- **Hot-Rebuild & In-Bot CI/CD**:
  - `/restart` — clean, non-blocking systemd service restart.
  - `/rebuild [branch] [--pull] [--force]` — pulls latest code from Git, compiles a fresh Go binary, atomically replaces the executable, restarts the service, and sends a Telegram notification with the new commit hash upon startup.

---

## 🏗 Architecture

```
┌──────────────────────────────────────────────────────────┐
│              Telegram Client (or other messenger)        │
└────────────────────────────┬─────────────────────────────┘
                             │  HTTPS / Long Polling
                             ▼
┌──────────────────────────────────────────────────────────┐
│  internal/adapters/telegram/  ──► ports.Transport impl.  │
│  (the only package importing gopkg.in/telebot.v3;        │
│   new messenger = new package here, following pattern)   │
└────────────────────────────┬─────────────────────────────┘
                             │ internal/ports (Messenger, Transport, Session)
                             ▼
┌──────────────────────────────────────────────────────────┐
│                 bro-bot (Go Executable)                  │
│                                                          │
│  internal/handlers/  ──► Commands & callbacks (agnostic   │
│                           via ports.Session)             │
│  internal/domain/    ──► Task Queue, Sessions & Tokens   │
│  internal/agents/    ──► Agent Registry & Mode Router    │
│  internal/storage/   ──► SQLite Persistence & Migrations │
│  internal/models/    ──► Model Registry & Aliases        │
│  internal/effort/    ──► Reasoning Effort Levels         │
│  internal/mcp/       ──► Built-in Stdio MCP Server       │
│  internal/tools/     ──► Workspace File & Shell Tools    │
│  internal/i18n/      ──► Message catalogs (locales/*.json)│
│  internal/system/    ──► Hot-Rebuild, Systemd & Top/PS   │
│  internal/utils/     ──► Rich-text & Markdown parser     │
└───────┬────────────────────────────────────────────┬─────────┘
        │ Mode: acp / mcp (CLI Agents)               │ Mode: api (Direct REST)
        ▼                                            ▼
┌────────────────────────────────────────┐   ┌────────────────────────────────┐
│ Agy / Grok (ACP) & Claude (MCP)        │   │ Direct API Clients:            │
│ • agy acp / grok agent stdio (HTTP MCP)│   │ • Gemini API                   │
│ • claude + bot mcp-serve (Socket)      │   │ • Anthropic Messages API       │
│ • NDJSON ACP & stream-json events      │   │ • File Tools (read/write/exec) │
└───────────────────┬────────────────────┘   └───────────────┬────────────────┘
                    │                                        │
                    └───────────────────┬────────────────────┘
                                        ▼
                           ┌─────────────────────────┐
                           │   Projects Root Dir     │
                           │   /home/deploy/projects │
                           │   ├── project-1/        │
                           │   └── project-2/        │
                           └────────────┬────────────┘
                                        │
                                        ▼ WAL Mode
                           ┌─────────────────────────┐
                           │   SQLite Database       │
                           │   data/bot.db           │
                           │   (Tasks, Plans, Logs)  │
                           └─────────────────────────┘
```

### Execution Modes Architecture

`bro-bot` abstracts agent execution behind `ports.Agent` and `agents.Registry`, supporting three operation modes:

1. **ACP Mode (`acp`, default for `agy` and `grok`)**:
   - `agy acp` (Google Antigravity) and `grok agent [--always-approve] [--model <id>] [--reasoning-effort <level>] stdio`.
   - Handshake: `initialize` (plus `authenticate` with `cached_token` or API-key method when required by `grok`), then `session/resume` or `session/new` (fallback when resume fails). `session/load` is not used so history is not replayed into Telegram.
   - Bot tools (`telegram_send_message`, `send_file`, `ask_user`, `report_progress`, `get_task_context`, `list_tasks`, `list_projects`, `sqlite_query`) are attached in `session/new` / `session/resume` via an ephemeral in-process HTTP MCP server (`127.0.0.1:0` + single-use `Authorization: Bearer` token) dispatching straight to `BotTools`, with no `bot mcp-serve` subprocess or Unix socket. Legacy `bro_bot` entries in `~/.gemini/config/mcp_config.json` and `~/.gemini/antigravity-cli/mcp_config.json` are cleaned up automatically so global configs do not shadow the session HTTP MCP endpoint.
   - In chat and planning (`ReadOnly`), `session/request_permission` allows `read`, `search`, `think`, and `fetch`, `bro_bot` MCP & question tools (including read-only `sqlite_query`), and whitelisted safe read-only shell commands (`ls`, `find`, `grep`/`rg`, `cat`/`head`/`tail`, read-only `git`, `docker ps`/`logs`, `systemctl status`, `journalctl`, analytical `python3` via `-c`/heredoc/`.py`, read-only `sqlite3`, and HTTP GET/HEAD via `curl`/`wget` to stdout or `/tmp`), while rejecting `write_file`/`edit_file`, `.sh` scripts, mutating commands, unsafe output redirections, and command substitutions (and automatically sending a single recovery prompt if an ACP turn ends empty after a permission denial). In tasks, `agy` approves file/shell actions automatically and `grok` approves with `allow_always` unless `GROK_ACP_YOLO` is turned off. Stopping a turn cancels the process group.
   - Bot tools available across ACP (`agy`/`grok`), MCP (`claude`), and API (`agy`/`claude`) modes:
     - `telegram_send_message`: sends a message (or quiet `silent` note) to the task or conversation chat with Markdown formatting;
     - `send_file`: sends a validated workspace file (`path`) or an in-memory generated text file (`filename` + `content`) to Telegram;
     - `ask_user`: asks one question or a structured multi-question batch (`questions[]` with optional `is_multi_select`) with interactive buttons. The tool does not block: the agent ends its turn cleanly, the task switches to *waiting for input*, and the answer arrives as the next message of the same agent session (`session/resume` / `--resume` / API history);
     - `report_progress`: updates the live task status, optionally with a completion percentage;
     - `get_task_context`: returns prompt, plan, pending additions, questions, and status for the current or referenced task;
     - `list_tasks` / `list_projects`: lists tasks across a project (`*` for all projects) and registered workspaces;
     - `sqlite_query`: executes a single read-only SQL query (`SELECT`, `WITH`, `EXPLAIN`, `VALUES`, or read-only `PRAGMA`) against a SQLite database inside the project workspace (`mode=ro` + `PRAGMA query_only=ON`, safe for live WAL databases, without requiring the `sqlite3` CLI).
   - If the bot is not running or the token/peer check fails, the tools return an error to the agent instead of pretending the action succeeded.

2. **MCP Mode (`mcp`, default for `claude`)**:
   - Local `claude` runs inside an isolated pseudo-terminal (PTY) with streaming NDJSON output (`stream-json`) and connects to the bot's built-in stdio Model Context Protocol (MCP) server (`bot mcp-serve` over JSON-RPC 2.0):
     - `claude --dangerously-skip-permissions --print --output-format stream-json [--resume <id>] --model <model> -p <prompt>`
   - `claude` connects using `--mcp-config` with an ephemeral config file that is removed after the process exits.
   - `claude` spawns `bot mcp-serve` as its child process, forwarding every tool call over `<BOT_DIR>/data/mcp/bridge.sock` (directory mode `0700`) authenticated with a per-step `ChannelToken` and `SO_PEERCRED` PID matching.

3. **API Mode (`api`)**:
   - Direct HTTP/REST communication with model provider endpoints:
     - Google Gemini API (`GEMINI_API_KEY`) via Google GenAI SDK;
     - Anthropic Messages API (`ANTHROPIC_API_KEY` / `CLAUDE_API_KEY`).
   - Integrated workspace tool engine (`internal/tools`) plus in-process bot tools:
     - Safe file system access (`read_file`, `write_file`, `edit_file`, `list_dir`, `grep_search`, `find_files`) with strict containment to the active project path.
     - Shell command execution (`run_command`) for pipeline tasks, and in-process access to `telegram_send_message`, `send_file`, `ask_user`, `report_progress`, `get_task_context`, `list_tasks`, `list_projects`, and `sqlite_query`.
     - Sandboxed read-only permissions in conversational mode (`/chat`) and planning mode (`/plan`), with full workspace manipulation during task execution (`/new` or approved `/plan`).

### Messenger Abstraction

All core logic (`internal/handlers`, `internal/domain`, `internal/system`) interacts with the messaging platform through abstract interfaces:
- `ports.Messenger`: sending/editing messages, sending documents, and managing inline keyboards.
- `ports.Transport`: routing commands, incoming text messages, voice, documents/photos, and inline callback queries.

The current implementation is `internal/adapters/telegram`, which isolates `gopkg.in/telebot.v3`. The `MESSENGER` environment variable (defaults to `telegram`) selects the active transport in `cmd/bot/main.go`. Adding support for Discord, Slack, or Mattermost simply requires implementing `ports.Transport` in `internal/adapters/<name>/` without altering handler code.

---

## 📦 System Requirements

- **Operating System**: Linux (Ubuntu 22.04+, Debian 11+, RHEL/Rocky 9+).
- **Go**: Version `1.23` or higher.
- **Git**: Installed and configured.
- **GitHub CLI (`gh`)**: Installed and authenticated (`gh auth login`) to enable automatic Pull Request creation.
- **SSH Keys**: Configured on GitHub / GitLab for cloning and pushing project repositories.
- **Agent CLI**: Google Antigravity CLI (`agy`), Claude Code CLI (`claude`), and/or Grok CLI (`grok`), installed, authenticated, and available in the user's `$PATH` (e.g., `/home/deploy/.local/bin/agy`). Grok needs `grok login` or `XAI_API_KEY`.
- **Telegram Bot Token**: Created via [@BotFather](https://t.me/BotFather).
- **Telegram User ID**: Numeric account ID (obtainable via [@userinfobot](https://t.me/userinfobot)).

---

## 🚀 Installation & Build

### Option A. Automated Server Deployment via Makefile (Recommended for Clean Servers)

A comprehensive `Makefile` is included to automate production deployment on a clean Linux server (Ubuntu/Debian). Messages are bilingual (English by default, with optional Russian localization):

```bash
# Run full deployment in English (default, must be executed as root)
sudo make install

# Run full deployment with Russian messages
sudo make install LANG=ru
# or: sudo make install L=ru
```

The `make install` command performs the following sequence:
1. **`step1-user`**: Installs essential system dependencies (`git`, `curl`, `wget`, `build-essential`, `sudo`, `python3`, `python3-pip`, `ffmpeg`), creates the `deploy` system user with passwordless `sudo`, and initializes workspace directories (`~/.local/bin`, `~/projects`).
2. **`step2-agents`** (or **`step2-agy`**): Checks and restores `agy`, installs Claude Code CLI, configures the `bro_bot` MCP server for `claude`, and removes legacy `bro_bot` entries from Gemini CLI MCP configs (`agy` connects via ACP with in-process HTTP MCP).
3. **`step3-service`**: Generates systemd unit `/etc/systemd/system/bro-bot.service` with automatic restart and `.env` support, then registers and enables the service.
4. **`step4-clone-build`**: Checks for Go 1.23+ (downloads and installs it if missing), clones or updates the repository at `/home/deploy/bro-bot`, prepares `.env` from `.env.example`, and compiles the `bot` binary.
5. **`step5-start`**: Restarts `bro-bot.service`, checks that it is active, and prints the current status.

After deployment, configure your bot token and parameters in `/home/deploy/bro-bot/.env` and restart the service:
```bash
sudo systemctl restart bro-bot.service
```

#### Useful Makefile Targets for Development:
- `make help` — displays help for all available targets (`make help LANG=ru` for Russian);
- `sudo make install-ffmpeg` — installs `ffmpeg` (required for voice message conversion);
- `sudo make install-whisper` — builds `whisper-server`, downloads `base-q5_1` and Silero VAD `silero-v6.2.0` models, and registers systemd service;
- `make build` — compiles the Go binary `./bot` (`go build -o bot ./cmd/bot`);
- `make test` — runs all project tests (`go test ./...`);
- `make run` — runs the bot directly from source (`go run ./cmd/bot`);
- `make clean` — removes the compiled `bot` binary.

#### Language Selection in Makefile:
Messages default to English (`en`). You can switch language dynamically for any target using `LANG=ru` or shorthand `L=ru`:
```bash
make help            # English (default)
make help LANG=ru    # Russian (or L=ru)
sudo make install LANG=ru
```

---

### Option B. Manual Installation & Build

#### 1. Clone the Repository

```bash
# Recommended installation directory: /home/deploy/bro-bot
cd /home/deploy
git clone git@github.com:ibrusi/bro-bot.git
cd bro-bot
```

#### 2. Create Projects Workspace Directory

Create the directory where your target projects will reside (default: `/home/deploy/projects`):

```bash
mkdir -p /home/deploy/projects
```

#### 3. Build the Application

Download dependencies and compile the Go executable:

```bash
go mod download
go build -o bot ./cmd/bot
```

The compiled `bot` binary will be created in the current directory.

---

## ⚙️ Configuration (.env)

Create a `.env` file in the root of the project using `.env.example` as a template:

```bash
cp .env.example .env
nano .env
```

### Environment Variables Table

| Variable | Required | Default | Description |
|---|:---:|:---:|---|
| `MESSENGER` | No | `telegram` | Messenger adapter from `internal/adapters/` (see [Messenger Abstraction](#messenger-abstraction)). Currently `telegram` is supported. |
| `TELEGRAM_BOT_TOKEN` | **Yes** | — | Telegram Bot API token from [@BotFather](https://t.me/BotFather). |
| `TELEGRAM_ADMIN_ID` | **Yes** | — | Numeric Telegram ID of the administrator. The bot only accepts commands and replies from this user. |
| `PROJECTS_ROOT` | **Yes** | — | Absolute path to the directory hosting managed Git project repositories. |
| `SCRIPTS_DIR` | No | `scripts` under `BOT_DIR` | Path to the directory containing custom scripts for `/script` (see [Custom scripts](#custom-scripts-script)). |
| `DEFAULT_PROJECT` | No | First folder in `PROJECTS_ROOT` | Name of the project directory active by default upon startup. |
| `DEFAULT_MODEL` | **Yes** | — | Default model for `agy` (e.g. `gemini-pro-orchestrator-neon`, `orchestrator-carbon`, `orchestrator-argon`, `neon`, `carbon`, `argon`, `hydrogen`, `sonnet`, `opus`). Aliases are resolved in the `agy` set, so `haiku` is not available. |
| `GEMINI_API_KEY` | No | — | Google AI Studio key used by the `agy` agent in `api` mode (`/mode api`). Without it the `api` mode is unavailable. |
| `GEMINI_API_MODEL` | No | Auto-selected | Explicit Gemini API model name for `api` mode (e.g. `gemini-2.5-flash`). When unset, the model is picked from the models the API actually exposes: the junior family among the senior ones (flash) at its highest available version. |
| `ANTHROPIC_API_KEY` | No | — | Anthropic key used by the `claude` agent in `api` mode (`/mode api`). Without it (and without `CLAUDE_API_KEY`) the `api` mode is unavailable for `claude`. |
| `CLAUDE_API_KEY` | No | — | Legacy name for the same variable: used when `ANTHROPIC_API_KEY` is unset. |
| `CLAUDE_API_MODEL` | No | Auto-selected | Explicit Claude API model name for `api` mode (e.g. `claude-sonnet-5`). When unset, the model is picked from `/v1/models`: the junior family among the senior ones (sonnet) at its highest available version. Retired model ids are automatically replaced with a live model of the same family. |
| `XAI_API_KEY` | No | — | xAI key for the `grok` CLI in `acp` mode. The bot does not read it; the `grok` process inherits the environment. A completed `grok login` takes precedence. |
| `GROK_ACP_YOLO` | No | on | Tool approval for Grok tasks. Empty or any value other than `false`, `0`, `no`, `off` passes `--always-approve` and `yoloMode`. Chat stays read-only either way. |
| `QUESTION_TIMEOUT` | **Yes** | — | Timeout waiting for user response to agent questions (`ask_question` in agy, MCP `ask_user`). Formats: `15m`, `300s`, `1h`, or seconds. When elapsed, the task pauses. |
| `STEP_TIMEOUT` | No | `1h` | Execution timeout for a single agent step. Formats: `1h`, `30m`, `3600s`, or seconds. When exceeded, the task is paused while preserving the session. |
| `AUTO_CONTINUE_MAX` | No | `2` | How many times in a row a step is continued automatically when the agent ends its turn without waiting for its own background commands (agy prints `root agent idle; waiting … background task(s)` and the agent never wakes up, and/or `terminating N background task(s) on exit`). The step continues in the same agent session. When the limit is reached, the task is paused with a «Resume» button instead of being reported as completed. `0` disables auto-continue. |
| `CHAT_TIMEOUT` | No | `1h` | Timeout for a single answer in conversational mode (`/chat`). Formats: `1h`, `30m`, `3600s`, or seconds. |
| `BOT_DIR` | No | Executable's directory | Path to the bot's source code for `/rebuild` and storing restart markers. When unset, the executable's directory is used — but only if a `go.mod` sits next to it. Otherwise the bot refuses to start and says so. |
| `BOT_SERVICE_NAME` | **Yes** | — | Name of the systemd service unit for `/restart` and `/rebuild`. |
| `SQLITE_DB_PATH` | No | `data/bot.db` under `BOT_DIR` | Path to the SQLite database file for persistent tasks, plans, logs, and settings. |
| `WHISPER_SERVER_URL` | No | `http://127.0.0.1:8080/inference` (in `.env.example`) | HTTP endpoint of the Whisper speech-to-text server (e.g. `whisper.cpp`, `faster-whisper`, OpenAI API). Supports `/inference` and `/v1/audio/transcriptions` with automatic 404 fallback. Leave empty to disable voice recognition. |
| `WHISPER_API_KEY` | No | — | Optional Bearer authorization token if your Whisper server requires authentication. |
| `WHISPER_MODEL` | No | `base-q5_1` | Model name sent to Whisper server. |
| `WHISPER_LANGUAGE` | No | `en` | Speech recognition language code (`en`, `ru`, `auto`, etc.). Defaults to `en`. To switch the language, for example to Russian, set `WHISPER_LANGUAGE=ru`. |
| `WHISPER_PROMPT` | No | — (empty) | Initial prompt / vocabulary hint to guide Whisper context and reduce phonetic distortion of domain terms. Managed via `.env` (pre-filled in `.env.example`). Leave empty or set to `none` to disable. |
| `WHISPER_TEMPERATURE` | No | `0.0` | Sampling temperature (`0.0` for deterministic greedy search, avoiding hallucinations). |
| `WHISPER_LOUDNORM` | No | `true` | Audio loudness normalization via ffmpeg (`-af loudnorm`) to enhance quiet voices and whispering. Set to `false` to disable. |
| `WHISPER_TIMEOUT` | No | `60s` | Timeout for the speech transcription HTTP request. Formats: `60s`, `2m`, or seconds. |
| `DEBUG` | No | `false` (empty) | Debug mode. When set to `true` or `1`, Telegram quote messages display performance metrics (STT Latency, Audio Duration, RTF). |

### Example `.env`

```dotenv
TELEGRAM_BOT_TOKEN=123456789:ABCdefGHIjklMNOpqrSTUvwxYZ
TELEGRAM_ADMIN_ID=123456789
PROJECTS_ROOT=/home/deploy/projects
DEFAULT_PROJECT=bro-bot
DEFAULT_MODEL=gemini-pro-orchestrator-neon
QUESTION_TIMEOUT=15m
STEP_TIMEOUT=1h
AUTO_CONTINUE_MAX=2
CHAT_TIMEOUT=1h
BOT_DIR=/home/deploy/bro-bot
BOT_SERVICE_NAME=bro-bot.service
SQLITE_DB_PATH=data/bot.db
WHISPER_SERVER_URL=http://127.0.0.1:8080/inference
WHISPER_MODEL=base-q5_1
WHISPER_LANGUAGE=en
WHISPER_PROMPT="debug mode, code, commit, pull request, git, bot, deploy, status, tasks, logs, review, build, release, terminal"
WHISPER_TEMPERATURE=0.0
WHISPER_LOUDNORM=true
DEBUG=
```

---

## 🛠 Systemd Setup & Autostart

For production reliability and to enable `/restart` and `/rebuild`, run the bot as a systemd service.

### 1. Create `/etc/systemd/system/bro-bot.service`

Create the service file using `sudo`:

```ini
[Unit]
Description=Telegram Agent Bridge Bot (Go)
After=network.target network-online.target
Wants=network-online.target

[Service]
Type=simple
User=deploy
Group=deploy
WorkingDirectory=/home/deploy/bro-bot

EnvironmentFile=/home/deploy/bro-bot/.env
# Ensure PATH contains go, agy, claude, and user binaries
Environment="PATH=/usr/local/go/bin:/home/deploy/go/bin:/home/deploy/.local/bin:/usr/bin:/bin"

ExecStart=/home/deploy/bro-bot/bot

Restart=always
RestartSec=5s

StartLimitIntervalSec=60
StartLimitBurst=5
TimeoutStopSec=15
KillMode=mixed
OOMPolicy=continue

[Install]
WantedBy=multi-user.target
```

### 2. Configure `sudoers` for Service Restart

The bot's `/restart` and `/rebuild` commands execute `sudo systemctl restart bro-bot.service`. Grant passwordless restart permissions to the user:

```bash
sudo visudo -f /etc/sudoers.d/bro-bot
```

Add the following rule (replace `deploy` with your actual system username):

```sudoers
deploy ALL=(ALL) NOPASSWD: /bin/systemctl restart bro-bot.service, /usr/bin/systemctl restart bro-bot.service
```

### 3. Start and Verify the Service

```bash
# Reload systemd daemon
sudo systemctl daemon-reload

# Enable and start the service
sudo systemctl enable --now bro-bot.service

# Check service status
sudo systemctl status bro-bot.service

# Follow real-time logs
journalctl -u bro-bot.service -f
```

---

## 💬 Bot Command Reference

The bot is operated via text messages and slash commands in Telegram.

### 💬 Conversation Memory per Agent and Mode

The conversation transcript lives in the bot's database and is independent of agent and mode,
while the agent session id is kept separately for each "agent + mode" pair:

| | `mcp` (default) | `api` |
|---|---|---|
| **agy** | agent's own session with MCP config (`mcp_config.json`) | transcript replayed from the bot's database |
| **claude** | agent's own session with MCP config (`--mcp-config`) | transcript replayed from the bot's database |

When you switch `/agent` or `/mode`, the new agent receives a short context of previous turns,
so the conversation continues. Reset it with `/chat new`.

> ⚠️ The read-only stance in conversational mode is a prompt instruction, not a sandbox.
> If you need a guarantee that nothing changes on disk, use a separate branch or a task.

### 📌 Task Management & Planning

| Command | Description | Example |
|---|---|---|
| `<message>` | In conversational mode (default) — a question to the agent with preserved context; a code-change request is offered as a plan or a task. With `/chatmode off` — creates a task right away. | `How does the task pipeline work?` |
| `/chat [question\|new\|stop]` | With no arguments — conversation status (project, agent, mode, memory kind). With text — a one-off question bypassing the classifier. `new` starts a fresh conversation, `stop` aborts the current answer. | `/chat show where commands are registered` |
| `/chatmode [on\|off]` | Answer plain messages in chat (`on`, default) or create a task immediately (`off`). | `/chatmode off` |
| `/plan [project] <task>` | Start a task in pre-planning mode (agent inspects repo, drafts a plan, and awaits approval). | `/plan Design distributed caching architecture` |
| `/planmode [on\|off]` | Toggle mandatory planning mode for all incoming tasks. | `/planmode on` |
| `/approve [id]` (or `/confirm`) | Approve the agent's proposed architectural plan and trigger implementation. | `/approve 3` |
| `/tasks` | List all tasks in the current session with statuses and quick focus buttons. | `/tasks` |
| `/task <id> [text]` | Switch focus to a specific task or append additional instructions. | `/task 2 update only the README` |
| `/add [id] <text>` | Send clarification or follow-up instructions to an active task. | `/add 1 also write unit tests for the handler` |
| `/new [project] [agent] <task>` | Explicitly create a new task (optionally specifying a different project or agent). | `/new my-service claude optimize SQL queries` |
| `/pause [id]` | Pause a running task (unblocks the project queue for other tasks). | `/pause 1` |
| `/resume [id] [answer]` | Resume a paused task (paused by question timeout or step timeout) preserving session context. | `/resume 1 use the second approach` |
| `/retry [id]` | Restart a task with a fresh agent session (resets `conversation_id`). | `/retry 2` |
| `/status [id]` | Comprehensive status of a task: model, session ID, duration, recent logs, queue info. | `/status 2` |
| `/history [id]` | View the full transcript of user and agent messages for a task session. | `/history 2` |
| `/cancel [id]` | Cancel and terminate task execution. | `/cancel 2` |

> 💡 **Tip:** You can reply to any bot notification about a task using standard Telegram **Reply** — the bot will route your response directly to that task! Multiline prompts and answers with code blocks or line breaks are preserved intact in `/new`, `/plan`, `/add`, `/resume`, and `/chat`.

---

### 📂 Project & Repository Management

| Command | Description | Example |
|---|---|---|
| `/project [name]` | List all available projects in `PROJECTS_ROOT` with the active one highlighted, or switch active working project. | `/project`, `/project my-backend-api` |
| `/clone <url> [name]` | Clone a repository via SSH or HTTPS into `PROJECTS_ROOT`. | `/clone git@github.com:org/repo.git my-repo` |

---

### 🧠 Models, Quotas & Resources

| Command | Description | Example |
|---|---|---|
| `/model [name\|refresh]` | List all available models with descriptions and aliases, refresh the catalog, or switch active model for future tasks. | `/model`, `/model flash`, `/model refresh` |
| `/effort [level]` | Show or set the agent reasoning effort: `auto`, `low`, `medium`, `high`, `xhigh`, `max`. Without a level — current value and selection buttons. | `/effort high` |
| `/agent [name]` | Switch the active agent engine (`agy`, `claude`, or `grok`). Grok selects `acp`; leaving Grok restores `mcp`. | `/agent grok` |
| `/mode [mcp|api|acp]` | Switch agent execution mode. `acp` is Grok over `grok agent stdio`. `mcp` and `api` are rejected while Grok is active. | `/mode acp` |
| `/usage` (or `/limits`) | Check remaining free quotas and paid credit balances for Antigravity. | `/usage` |
| `/tokens` (or `/stats`) | Token usage stats for the task: Input, Output, Thinking, Cache Read, and Hit Rate. | `/tokens` |
| `/context [id]` | Visual breakdown of model context window utilization. | `/context` |
| `/top` (or `/ps`, `/resources`) | Real-time server telemetry: CPU, RAM, disk space, Load Average, cgroup, agent processes (`agy`, `claude`, `grok`), and `whisper-server`. | `/top` |

#### Reasoning effort (`/effort`)

- One level is shared by every agent and mode, like the active model; it is stored in SQLite (`effort` setting) and restored after a restart.
- `auto` (default) passes no level: the agent and the model use their own defaults, exactly as before. `low` → `max` trade answer speed and token spend for reasoning depth.
- The level applies from the next agent run: a running task step or chat answer keeps the level it started with.
- How the level reaches the agent:

| Agent | `mcp` (default) | `api` | `acp` |
|---|---|---|---|
| `claude` | `--effort <level>` flag of Claude Code | `output_config.effort` of the Messages API — only when `/v1/models` confirms the model supports this level (for example, `xhigh` requires Opus 4.7+, Haiku 4.5 has no effort at all); otherwise the model default is used and the bot logs it | Not supported |
| `agy` | The level is passed as the exact model variant from `agy models`: `/effort low` with `gemini-3.1-pro-high` runs `gemini-3.1-pro-low`, and the bare family name (`claude-opus-5-5`) gets a level suffix (with `auto` — `medium`, or `high` if the family has no `medium`). If the family lacks the requested level, the highest level not above it is used, otherwise the lowest available (`/effort max` → `-high`). Models outside the catalog get `--effort <level>` as is, legacy Claude models (`claude-sonnet-4-6`) get none | Not supported: the Gemini SDK in use cannot configure reasoning, so `/effort` warns and the model default is used | Not supported |
| `grok` | Not supported | Not supported | `--reasoning-effort` and `session/set_config_option` (`reasoning_effort`). `max` is sent as `xhigh`. `auto` sends nothing |

---

### ⚙️ Bot Process Management

| Command | Description | Example |
|---|---|---|
| `/restart` | Safely restart the bot service via `systemctl`. | `/restart` |
| `/rebuild [branch] [--pull] [--force]` | Pull Git updates, rebuild the Go binary, and restart the bot. | `/rebuild main --pull` |
| `/language` | Choose the interface language (buttons are built from the installed catalogs). | `/language` |
| `/script [name] [args]` | Run a script from `SCRIPTS_DIR` by its file name with extension, passing the arguments; the output updates live and the run can be stopped with a button. Without a name — list available scripts. | `/script deploy/restart.sh --force` |

#### Custom scripts (`/script`)

- `/script` with no arguments lists every script in `SCRIPTS_DIR` (nested folders included, hidden files skipped).
- `/script <name> [args...]` runs the script by its file name **with the extension**, e.g. `/script deploy/restart.sh --force`. Everything after the name is passed to the script as arguments, split by spaces (no shell is involved, so quotes and `$VARS` are passed literally).
- While the script runs, the bot message shows the tail of its output (stdout and stderr) and refreshes every 5 seconds. When it ends, the message is replaced with the result: finished, failed with the exit status, or stopped.
- There is no time limit. The **⏹ Stop** button under the message stops the run: the script and all its child processes get `SIGTERM`, and those still alive after 5 seconds get `SIGKILL`.
- An executable file (`chmod +x`) runs directly, so any language works via its shebang. A non-executable file runs with the interpreter matching its extension: `.sh`/`.bash` → `bash`, `.py` → `python3`, `.js` → `node`, `.rb` → `ruby`, `.pl` → `perl`, `.php` → `php`.
- The script's working directory is the folder it lives in.
- Scripts are server-local: the `scripts` folder is kept in the repository, but its contents are ignored by Git (`.gitignore`), so put your scripts there on each server yourself.

---

## 🌍 Interface Language & Localization

Every user-facing string — replies, inline buttons, command descriptions in the messenger menu, error messages and the prompts sent to the coding agent — is looked up in a message catalog instead of being hardcoded.

### Switching the language

- `/language` shows one button per installed catalog; the active language is marked with a check mark.
- The choice is stored in SQLite under the `bot_language` setting and restored on the next start.
- Until a language is chosen, the bot answers in **English** (`i18n.Default`). An unknown or removed language code falls back to English instead of leaving the bot without one.
- Switching the language also re-registers the messenger command menu for every installed catalog, so `/`-hints follow the interface.

The agent prompts are localized too: planning, implementation and chat-mode instructions are taken from the same catalog, so the agent writes its plans and reports in the selected language.

### Adding a new language

Adding a language requires **no Go code changes** — drop one file into `internal/i18n/locales/`:

1. Copy `internal/i18n/locales/en.json` to `internal/i18n/locales/<code>.json`, where `<code>` is the two-letter language code (`de`, `fr`, `es`, …). The file name and `meta.code` must match.
2. Fill in `meta`:

   ```json
   {
     "meta": { "code": "de", "name": "Deutsch", "flag": "🇩🇪" },
     "messages": { "...": "..." }
   }
   ```

   `name` is the native language name and `flag` is the emoji — together they become the button label in `/language`.
3. Translate every value under `messages`. Keep the placeholders (`%s`, `%d`, `%.1f`) in the same order as in `en.json`, and keep the HTML tags (`<b>`, `<code>`, `<a href="…">`) intact.
4. Run the catalog tests:

   ```bash
   go test ./internal/i18n/
   ```

   They fail with an explicit list of problems if a key is missing, a key was invented that the default catalog does not have, or the placeholders diverge from the English original.
5. Rebuild the bot. The catalogs are embedded with `go:embed`, so the new language appears in `/language`, in the command menu and in the fallback chain automatically.

A partially translated catalog still works: a missing key falls back to English rather than breaking the message — but the tests will point it out.

---

## 📋 Agent Guidelines (AGENTS.md)

Projects managed by `bro-bot` utilize an **`AGENTS.md`** file (symlinked as **`AGENTS.md`**) in their repository root. The agent CLI automatically follows these instructions:

1. **Planning Mode**:
   - In planning mode, the agent creates no branches, alters no code, and formulates a step-by-step implementation plan for user review.
2. **Feature Branching**:
   - Every task branches from up-to-date `main`: `feat/short-description` or `fix/short-description`.
3. **Quality & Testing**:
   - Changes must include test coverage and pass linters (`go test ./...`, `go vet ./...`).
4. **Conventional Commits**:
   - Commits follow the **Conventional Commits** specification (`feat: ...`, `fix: ...`, `docs: ...`, `refactor: ...`).
5. **Pull Request & Completion**:
   - The branch is pushed to origin: `git push -u origin HEAD`.
   - A PR is created via GitHub CLI: `gh pr create --fill`.
   - The agent prints a concluding marker:
     ```text
     PR_URL: <full link to the created PR>
     ```
   - The bot intercepts this URL via regex and sends a clickable link directly to the Telegram chat!

> 💡 When cloning a new project with `/clone`, the bot automatically checks for `AGENTS.md` and copies it from `PROJECTS_ROOT` if absent.

---

## 🧪 Development & Testing

### Local Run (Development Mode)

```bash
# Export environment variables from .env and run
set -a
source .env
set +a
go run ./cmd/bot
```

### Running Tests

The codebase includes test coverage across all packages (`handlers`, `models`, `system`, `domain`, `adapters`, `utils`):

```bash
# Run all project tests
go test -v ./...

# Run static analysis
go vet ./...

# Check test coverage
go test -cover ./...
```

### Versioning and Releases (CI/CD)

The project adheres to [Semantic Versioning](https://semver.org/) and [Keep a Changelog](https://keepachangelog.com/).

- **Single Source of Truth**: The root [`VERSION`](file:///home/deploy/projects/bro-bot/VERSION) file defines the current release version (e.g. `1.0.0` or `1.1.0-beta.1`).
- **CLI Version Inspection**:
  ```bash
  ./bot -version
  ```
- **Automated Verification and Releases (CI/CD)**:
  - **Pull Request CI**: Automated test suite (`go mod verify`, `go vet`, unit tests, race detector, binary build) runs in [`ci.yml`](.github/workflows/ci.yml) for all PRs and commits to `main`.
  - **Release Pipeline**: When code is merged into `main`, the standalone release workflow [`release.yml`](.github/workflows/release.yml) checks [`VERSION`](file:///home/deploy/projects/bro-bot/VERSION) against published releases on GitHub.
  - If a new version is detected:
    - Pre-release verification is performed.
    - Pre-compiled binaries are cross-compiled for `linux/amd64` and `linux/arm64`.
    - Tarball archives and SHA256 checksums (`checksums.txt`) are generated.
    - Release notes for the version are extracted from [`CHANGELOG.md`](file:///home/deploy/projects/bro-bot/CHANGELOG.md).
    - A GitHub Release is created via GitHub CLI.
- **Beta / Pre-release Versions**:
  - Versions containing a hyphen (e.g. `1.1.0-beta.1`, `2.0.0-rc.1`) are automatically published with the `--prerelease` flag and marked with the GitHub Pre-release badge.
  - Releases can also be dispatched manually via the GitHub Actions UI (`workflow_dispatch`) with custom options (`version`, `prerelease: auto/true/false`, and `dry_run`).

---

## 🔒 Security

1. **Strict Telegram ID Authorization**:
   - Bot middleware verifies `c.Sender().ID == config.AdminID`.
   - All messages, commands, or inline callbacks from unauthorized accounts are dropped silently.
2. **Path Traversal Protection**:
   - Project name sanitization (`sanitizeProjectName`) and `filepath.Clean` prevent path traversal attacks (`../`).
   - Project identifiers are restricted to safe characters: `[a-zA-Z0-9_\.\-]`.
3. **Command Injection Prevention**:
   - Subprocesses (`git`, `go`, `systemctl`, `agy`) are executed directly via `exec.CommandContext` without an intervening shell (`sh -c`).
   - URL and path arguments are isolated with the `--` delimiter.


---

## 📄 License

This project is licensed under the MIT License. See [LICENSE](LICENSE) for details.
