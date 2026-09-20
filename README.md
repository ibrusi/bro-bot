# 🤖 bro-bot (Bro Bot)

[![Go Version](https://img.shields.io/badge/Go-1.23+-00ADD8?style=flat&logo=go)](https://golang.org/)
[![License](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)
[![Platform](https://img.shields.io/badge/platform-Linux-lightgrey.svg)](https://kernel.org)

**English** | [Русский](README.ru.md)

**bro-bot** is an autonomous Go-based service bridge connecting **Telegram** with agentic developer environments: **Google Antigravity CLI (`agy`)** and **Claude Code CLI (`claude`)**.

The bot empowers a developer or engineering team to manage a pool of projects, assign coding and refactoring tasks, approve interactive architectural plans, answer clarifying agent questions, inspect session transcripts, track token spending, and monitor server resource usage — all without leaving Telegram.

<a id="disclaimer"></a>
> [!WARNING]
> **Safety and Usage Disclaimer**:
> - **Autonomous Command Execution & Elevated Privileges**: `bro-bot` executes AI agent CLIs (`agy`, `claude`) with elevated system permissions (`--dangerously-skip-permissions`). Autonomous agents can run shell commands, create, modify, or delete files, install dependencies, and push Git changes without manual approval for every action. Always run the bot under a dedicated non-root user account (e.g., `deploy`), restrict `sudoers` privileges, and strictly limit bot access via `TELEGRAM_ADMIN_ID` (never run the bot as `root`).
> - **Account Suspension Risk with Subscription Plans (CLI Mode)**: Running CLI agents authenticated with personal subscriptions (Claude Pro/Team/Max, Google One AI Premium/Gemini Advanced) in automated bot workflows (`cli` mode) violates the Terms of Service of those providers (prohibition against automated or unattended use of non-API interfaces). Using `cli` mode is strictly at your own risk and may lead to account penalties or bans. In contrast, operating via official API keys (`api` mode) fully complies with provider policies and will not result in account bans.
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
  - Instant switching between active project workspaces (`/projects`, `/use <name>`).
  - On-the-fly Git cloning over SSH or HTTPS (`/clone <url> [name]`) with automatic provisioning of the `AGENTS.md` rules template.
- **Conversational Mode (default)**:
  - A plain message is a conversation with the agent about the active project: context is preserved between messages, so `/resume` is no longer needed.
  - When a message looks like a code-change request, the bot does not run it silently — it offers buttons: answer in chat, draft a plan, or create a task.
  - Works for every agent (`agy`, `claude`) and every execution mode (`mcp`, `cli`, `api`); switching `/agent` or `/mode` mid-conversation keeps the thread.
  - In chat the agent only reads the code and answers: file edits, branches, commits and PRs go through a task.
  - Controls: `/chat` (status), `/chat <question>`, `/chat new`, `/chat stop`, `/chatmode [on|off]`.
- **Intelligent Task Pipeline**:
  - Independent, per-project task queues.
  - Pre-planning mode (`/plan <task>`, `/planmode [on|off]`): the agent inspects the repository, composes and justifies an architectural plan, awaits user confirmation via interactive inline buttons (`/approve`, `/confirm`), and only then starts writing code.
  - Pause and resume (`/pause [id]`, `/resume [id] [answer]`).
  - Live execution streaming, recent logs, and step tracking (`/status [id]`, `/tasks`).
  - Dialogue transcript inspection (`/history [id]`): view the full conversation and agent actions directly from the session logs.
  - Safe cancellation of stuck or outdated tasks (`/cancel [id]`).
- **Interactive Dialogues & Clarifying Questions (`ask_question`)**:
  - When the agent requires clarification, the bot presents a multi-choice poll with buttons directly in Telegram.
  - Flexible reply options: plain text messages, Telegram Reply to task notifications, or the `/add [id] <text>` command.
  - Configurable timeout (`QUESTION_TIMEOUT`): if no response is received in time, the task automatically pauses to unblock the project queue for other tasks.
- **Dynamic Model Registry**:
  - Automatic discovery of available models from the CLI (`agy models`) with disk caching and fallback mechanisms.
  - Convenient aliases: `default`, `flash`, `flash-high`, `flash-low`, `pro`, `pro-low`, `sonnet`, `opus`, `oss`.
  - Instant model switching (`/models`, `/model <name|alias>`).
  - Multi-agent toggle (`/agent <agy|claude>`).
- **Quota, Token & Resource Monitoring**:
  - `/usage` (or `/limits`) — live quota limits and credit balances (`agy /quota`, `agy /credits`).
  - `/tokens` (or `/stats`) — granular token statistics for the current/completed task: Input, Output, Thinking, Cache Read, and Cache Hit Rate.
  - `/context [id]` — visual breakdown of context window utilization (system prompts, conversation history, tool outputs).
  - `/top` (or `/ps`, `/resources`) — real-time server telemetry: CPU, RAM, free disk space, Load Average, systemd cgroup metrics, and active `agy`/`claude` processes.
- **Execution Modes: MCP, CLI & API (`/mode [mcp|cli|api]`)**:
  - `mcp` (default): integrates agents with a built-in Model Context Protocol (MCP) server over stdio JSON-RPC 2.0 (`claude` via `--mcp-config`, `agy` via `mcp_config.json`), providing authorized channels and tools (`telegram_send_message`, `ask_user`, `report_progress`).
  - `cli`: spawns local `agy` and `claude` CLI tools via PTY.
  - `api`: connects directly to provider APIs (Google Gemini API & Anthropic Messages API) with built-in workspace file tools (`read_file`, `write_file`, `edit_file`, `list_dir`, `run_command`).
  - Safe sandboxing: chat mode (`/chat`) provides strictly read-only file access, while file writes and command executions are enabled for pipeline tasks. All paths are validated within the project directory.
- **Multilingual Interface**:
  - Every bot reply, button, command description and agent prompt comes from a message catalog — English and Russian ship out of the box.
  - English is the default until a language is picked; the choice is made with `/language` and survives restarts.
  - A new language is one JSON file in `internal/i18n/locales/` — no Go code changes (see [Interface Language & Localization](#-interface-language--localization)).
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
│  internal/storage/   ──► SQLite Persistence & Migrations │
│  internal/models/    ──► Model Registry & Aliases        │
│  internal/i18n/      ──► Message catalogs (locales/*.json)│
│  internal/system/    ──► Hot-Rebuild, Systemd & Top/PS   │
│  internal/utils/     ──► Rich-text (HTML subset) &       │
│                           Markdown parser                │
└──────────────┬─────────────────────────────┬─────────────┘
               │ PTY (Pseudo-Terminal)       │ Git / FS
               ▼                             ▼
┌──────────────────────────────┐ ┌─────────────────────────┐
│   Antigravity CLI (agy)      │ │   Projects Root Dir     │
│   Claude Code CLI (claude)   │ │   /home/deploy/projects │
│  --stream-json / subagents   │ │   ├── project-1/        │
│  Autonomous Coding Agent     │ │   └── project-2/        │
└──────────────────────────────┘ │                         │
                                 └─────────────────────────┘
                                             ▲
                                             │ WAL Mode
                                 ┌─────────────────────────┐
                                 │   SQLite Database       │
                                 │   data/bot.db           │
                                 └─────────────────────────┘
```

Each task runs inside an isolated pseudo-terminal (PTY) with invocation flags:
`agy --dangerously-skip-permissions --print-timeout 30m --output-format stream-json [--conversation <id>] --model <model> -p <prompt>`

The bot parses the NDJSON `stream-json` stream, intercepts thinking phases, tool calls, and completion events, and formats them into intuitive Telegram updates.

### Messenger Abstraction

All core logic (`internal/handlers`, `internal/domain`, `internal/system`) interacts with the messaging platform through abstract interfaces:
- `ports.Messenger`: sending/editing messages, sending documents, and managing inline keyboards.
- `ports.Transport`: routing commands, incoming text messages, and inline callback queries.

The current implementation is `internal/adapters/telegram`, which isolates `gopkg.in/telebot.v3`. The `MESSENGER` environment variable (defaults to `telegram`) selects the active transport in `cmd/bot/main.go`. Adding support for Discord, Slack, or Mattermost simply requires implementing `ports.Transport` in `internal/adapters/<name>/` without altering handler code.

---

## 📦 System Requirements

- **Operating System**: Linux (Ubuntu 22.04+, Debian 11+, RHEL/Rocky 9+).
- **Go**: Version `1.23` or higher.
- **Git**: Installed and configured.
- **GitHub CLI (`gh`)**: Installed and authenticated (`gh auth login`) to enable automatic Pull Request creation.
- **SSH Keys**: Configured on GitHub / GitLab for cloning and pushing project repositories.
- **Agent CLI**: Google Antigravity CLI (`agy`) and/or Claude Code CLI (`claude`), installed, authenticated, and available in the user's `$PATH` (e.g., `/home/deploy/.local/bin/agy`).
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
1. **`step1-user`**: Installs essential system dependencies (`git`, `curl`, `wget`, `build-essential`, `sudo`, `python3`, `python3-pip`), creates the `deploy` system user with passwordless `sudo`, and initializes workspace directories (`~/.local/bin`, `~/projects`).
2. **`step2-agents`** (or **`step2-agy`**): Checks and restores `agy`, installs Claude Code CLI, and configures the `bro_bot` MCP server for both agents.
3. **`step3-service`**: Generates systemd unit `/etc/systemd/system/bro-bot.service` with automatic restart and `.env` support, then registers and enables the service.
4. **`step4-clone-build`**: Checks for Go 1.23+ (downloads and installs it if missing), clones or updates the repository at `/home/deploy/bro-bot`, prepares `.env` from `.env.example`, and compiles the `bot` binary.
5. **`step5-start`**: Restarts `bro-bot.service`, checks that it is active, and prints the current status.

After deployment, configure your bot token and parameters in `/home/deploy/bro-bot/.env` and restart the service:
```bash
sudo systemctl restart bro-bot.service
```

#### Useful Makefile Targets for Development:
- `make help` — displays help for all available targets (`make help LANG=ru` for Russian);
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
| `SCRIPTS_DIR` | No | `scripts` under `BOT_DIR` | Path to the directory containing custom scripts for `/script`. |
| `DEFAULT_PROJECT` | No | First folder in `PROJECTS_ROOT` | Name of the project directory active by default upon startup. |
| `DEFAULT_MODEL` | **Yes** | — | Default model for `agy` (e.g. `gemini-3.1-pro-high`, `flash`, `sonnet`, `opus`). |
| `GEMINI_API_KEY` | No | — | Google AI Studio key used by the `agy` agent in `api` mode (`/mode api`). Without it the `api` mode is unavailable. |
| `GEMINI_API_MODEL` | No | Auto-selected | Explicit Gemini API model name for `api` mode (e.g. `gemini-2.5-flash`). When unset, the model is picked from the models the API actually exposes: the junior family among the senior ones (flash) at its highest available version. |
| `ANTHROPIC_API_KEY` | No | — | Anthropic key used by the `claude` agent in `api` mode (`/mode api`). Without it (and without `CLAUDE_API_KEY`) the `api` mode is unavailable for `claude`. |
| `CLAUDE_API_KEY` | No | — | Legacy name for the same variable: used when `ANTHROPIC_API_KEY` is unset. |
| `CLAUDE_API_MODEL` | No | Auto-selected | Explicit Claude API model name for `api` mode (e.g. `claude-sonnet-5`). When unset, the model is picked from `/v1/models`: the junior family among the senior ones (sonnet) at its highest available version. Retired model ids are automatically replaced with a live model of the same family. |
| `QUESTION_TIMEOUT` | **Yes** | — | Timeout waiting for user response to agent questions (`ask_question`). Formats: `15m`, `300s`, `1h`, or seconds. When elapsed, the task pauses. |
| `STEP_TIMEOUT` | No | `30m` | Execution timeout for a single agent step (`--print-timeout`). Formats: `30m`, `1h`, `1800s`, or seconds. When exceeded, the task is paused while preserving the session. |
| `CHAT_TIMEOUT` | No | `5m` | Timeout for a single answer in conversational mode (`/chat`). Formats: `5m`, `300s`, or seconds. |
| `BOT_DIR` | No | Executable's directory | Path to the bot's source code for `/rebuild` and storing restart markers. When unset, the executable's directory is used — but only if a `go.mod` sits next to it. Otherwise the bot refuses to start and says so. |
| `BOT_SERVICE_NAME` | **Yes** | — | Name of the systemd service unit for `/restart` and `/rebuild`. |
| `SQLITE_DB_PATH` | No | `data/bot.db` under `BOT_DIR` | Path to the SQLite database file for persistent tasks, plans, logs, and settings. |

### Example `.env`

```dotenv
TELEGRAM_BOT_TOKEN=123456789:ABCdefGHIjklMNOpqrSTUvwxYZ
TELEGRAM_ADMIN_ID=123456789
PROJECTS_ROOT=/home/deploy/projects
DEFAULT_PROJECT=bro-bot
DEFAULT_MODEL=gemini-3.1-flash-high
QUESTION_TIMEOUT=15m
STEP_TIMEOUT=30m
CHAT_TIMEOUT=5m
BOT_DIR=/home/deploy/bro-bot
BOT_SERVICE_NAME=bro-bot.service
SQLITE_DB_PATH=data/bot.db
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

| | `mcp` (default) | `cli` | `api` |
|---|---|---|---|
| **agy** | agent's own session with MCP config (`mcp_config.json`) | the agent's own session (`--conversation`) | transcript replayed from the bot's database |
| **claude** | agent's own session with MCP config (`--mcp-config`) | the agent's own session (`--resume`) | transcript replayed from the bot's database |

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

> 💡 **Tip:** You can reply to any bot notification about a task using standard Telegram **Reply** — the bot will route your response directly to that task!

---

### 📂 Project & Repository Management

| Command | Description | Example |
|---|---|---|
| `/projects` | List all available projects in `PROJECTS_ROOT` with the active one highlighted. | `/projects` |
| `/use <name>` | Switch active working project. | `/use my-backend-api` |
| `/clone <url> [name]` | Clone a repository via SSH or HTTPS into `PROJECTS_ROOT`. | `/clone git@github.com:org/repo.git my-repo` |

---

### 🧠 Models, Quotas & Resources

| Command | Description | Example |
|---|---|---|
| `/models` | List all available models from the agent CLI with descriptions and aliases. | `/models` |
| `/model [name]` | Switch active model for future tasks. | `/model flash` or `/model claude-sonnet-4-6` |
| `/agent [name]` | Switch the active agent engine (`agy` or `claude`). | `/agent claude` |
| `/mode [mcp|cli|api]` | Switch agent execution mode (MCP channel integration, local CLI, or direct API with workspace file tools). | `/mode mcp` |
| `/usage` (or `/limits`) | Check remaining free quotas and paid credit balances for Antigravity. | `/usage` |
| `/tokens` (or `/stats`) | Token usage stats for the task: Input, Output, Thinking, Cache Read, and Hit Rate. | `/tokens` |
| `/context [id]` | Visual breakdown of model context window utilization. | `/context` |
| `/top` (or `/ps`, `/resources`) | Real-time server telemetry: CPU, RAM, disk space, Load Average, cgroup, and agent processes. | `/top` |

---

### ⚙️ Bot Process Management

| Command | Description | Example |
|---|---|---|
| `/restart` | Safely restart the bot service via `systemctl`. | `/restart` |
| `/rebuild [branch] [--pull] [--force]` | Pull Git updates, rebuild the Go binary, and restart the bot. | `/rebuild main --pull` |
| `/language` | Choose the interface language (buttons are built from the installed catalogs). | `/language` |
| `/script <name>` | Execute a custom script from `SCRIPTS_DIR` and print output. | `/script deploy.sh` |

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
