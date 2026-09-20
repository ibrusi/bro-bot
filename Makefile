SHELL := /bin/bash
DEPLOY_USER := deploy
BOT_NAME := bro-bot
REPO_URL := https://github.com/ibrusi/bro-bot.git
REPO_DIR := /home/$(DEPLOY_USER)/$(BOT_NAME)
BIN_DIR := /home/$(DEPLOY_USER)/.local/bin
SERVICE_FILE := /etc/systemd/system/$(BOT_NAME).service

# =============================================================
# Language detection (default: en)
# Supports:
#   make <target>             -> default (en)
#   make <target> LANG=ru     -> ru (or LANG=en)
#   make <target> L=ru        -> ru (or L=en)
#   make <target> MSG_LANG=ru -> ru
# Note: environment variable LANG (e.g. ru_RU.UTF-8) is ignored
# unless passed explicitly on the command line.
# =============================================================
L ?=
MSG_LANG ?= en

ifeq ($(L),ru)
  ACTIVE_LANG := ru
else ifeq ($(L),en)
  ACTIVE_LANG := en
else ifeq ($(origin LANG),command line)
  ifeq ($(findstring ru,$(LANG)),ru)
    ACTIVE_LANG := ru
  else
    ACTIVE_LANG := en
  endif
else ifeq ($(origin MSG_LANG),command line)
  ifeq ($(findstring ru,$(MSG_LANG)),ru)
    ACTIVE_LANG := ru
  else
    ACTIVE_LANG := en
  endif
else
  ACTIVE_LANG := en
endif

# =============================================================
# Bilingual Messages
# =============================================================
ifeq ($(ACTIVE_LANG),ru)
  MSG_HELP_TITLE             := Доступные команды Makefile:
  MSG_HELP_DEPLOY_SECTION    := Развертывание на сервере (запускать под root на чистом сервере):
  MSG_HELP_INSTALL           := sudo make install           - Полное развертывание (все 5 шагов)
  MSG_HELP_STEP1             := sudo make step1-user        - Шаг 1: Создание пользователя $(DEPLOY_USER), окружения и пакетов
  MSG_HELP_STEP2             := sudo make step2-agy         - Шаг 2: Проверка/откат agy и проверка сигнатур manager.py
  MSG_HELP_STEP3             := sudo make step3-service     - Шаг 3: Настройка и регистрация systemd-службы $(BOT_NAME)
  MSG_HELP_STEP4             := sudo make step4-clone-build - Шаг 4: Клонирование репозитория, настройка .env и сборка Go
  MSG_HELP_STEP5             := sudo make step5-start       - Шаг 5: Запуск и проверка статуса службы $(BOT_NAME).service
  MSG_HELP_DEV_SECTION       := Разработка и сборка (для локальной работы):
  MSG_HELP_BUILD             := make build                  - Компиляция Go-бинарника (./cmd/bot)
  MSG_HELP_TEST              := make test                   - Запуск всех тестов проекта
  MSG_HELP_RUN               := make run                    - Запуск бота из исходников
  MSG_HELP_CLEAN             := make clean                  - Удаление скомпилированного бинарника
  MSG_HELP_LANG_SECTION      := Выбор языка сообщений (по умолчанию en):
  MSG_HELP_LANG_EN           := make <цель> LANG=en         - Вывод на английском языке (или L=en)
  MSG_HELP_LANG_RU           := make <цель> LANG=ru         - Вывод на русском языке (или L=ru)

  MSG_BUILD                  := ==> Компиляция Go-бинарника...
  MSG_TEST                   := ==> Запуск тестов...
  MSG_RUN                    := ==> Запуск бота...
  MSG_CLEAN                  := ==> Очистка артефактов сборки...

  MSG_INSTALL_DONE_TITLE     := ✅ Развертывание полностью завершено!
  MSG_INSTALL_DONE_RUNNING   := Служба $(BOT_NAME).service запущена.
  MSG_INSTALL_DONE_ENV       := Не забудьте проверить токен бота в: $(REPO_DIR)/.env

  MSG_STEP1_TITLE            := ==> [Шаг 1] Настройка окружения и пользователя $(DEPLOY_USER)...
  MSG_STEP1_USER_EXISTS      := Пользователь $(DEPLOY_USER) уже существует.
  MSG_STEP1_USER_CREATED     := Пользователь $(DEPLOY_USER) создан и добавлен в sudoers.

  MSG_STEP2_TITLE            := ==> [Шаг 2] Проверка agy и запуск manager.py...
  MSG_STEP2_RESTORE_BAK      := Восстанавливаем agy из agy.bak...
  MSG_STEP2_RUN_MANAGER      := Запуск manager.py patch cli...
  MSG_STEP2_SKIP_PATCH       := Пропуск патча (уже пропатчен или не требуется)

  MSG_STEP3_TITLE            := ==> [Шаг 3] Настройка systemd-сервиса $(BOT_NAME)...
  MSG_STEP3_CREATING         := Создаем $(SERVICE_FILE)...
  MSG_STEP3_ALREADY_EXISTS   := Служба $(BOT_NAME).service уже зарегистрирована.

  MSG_STEP4_TITLE            := ==> [Шаг 4] Клонирование $(REPO_URL), настройка .env и сборка бота...
  MSG_STEP4_INSTALL_GO       := Установка Go 1.23...
  MSG_STEP4_CLONE_REPO       := Клонируем репозиторий...
  MSG_STEP4_UPDATE_REPO      := Репозиторий уже склонирован, обновляем ветку...
  MSG_STEP4_COPY_ENV         := Копируем .env.example в .env...
  MSG_STEP4_WARN_ENV         := Предупреждение: .env.example не найден, создаем пустой .env
  MSG_STEP4_KEEP_ENV         := Файл .env уже существует, сохраняем существующий.
  MSG_STEP4_BUILD_BIN        := Компиляция Go-бинарника...

  MSG_STEP5_TITLE            := ==> [Шаг 5] Запуск и проверка службы $(BOT_NAME).service...
  MSG_STEP5_ACTIVE           := ✅ Служба активна (running)
  MSG_STEP5_FAILED           := ⚠️ Служба не смогла запуститься (проверьте значения в .env)
else
  MSG_HELP_TITLE             := Available Makefile targets:
  MSG_HELP_DEPLOY_SECTION    := Server deployment (run as root on a clean server):
  MSG_HELP_INSTALL           := sudo make install           - Complete deployment (all 5 steps)
  MSG_HELP_STEP1             := sudo make step1-user        - Step 1: Create $(DEPLOY_USER) user, base tools, and directories
  MSG_HELP_STEP2             := sudo make step2-agy         - Step 2: Restore/verify agy CLI and apply manager.py patch
  MSG_HELP_STEP3             := sudo make step3-service     - Step 3: Configure and register $(BOT_NAME) systemd service
  MSG_HELP_STEP4             := sudo make step4-clone-build - Step 4: Clone repository, configure .env, and build Go binary
  MSG_HELP_STEP5             := sudo make step5-start       - Step 5: Start and verify $(BOT_NAME).service status
  MSG_HELP_DEV_SECTION       := Development & build (local usage):
  MSG_HELP_BUILD             := make build                  - Compile Go binary (./cmd/bot)
  MSG_HELP_TEST              := make test                   - Run all project tests
  MSG_HELP_RUN               := make run                    - Run bot from source
  MSG_HELP_CLEAN             := make clean                  - Remove compiled binary
  MSG_HELP_LANG_SECTION      := Message language selection (default: en):
  MSG_HELP_LANG_EN           := make <target> LANG=en       - Output in English (or L=en)
  MSG_HELP_LANG_RU           := make <target> LANG=ru       - Output in Russian (or L=ru)

  MSG_BUILD                  := ==> Compiling Go binary...
  MSG_TEST                   := ==> Running tests...
  MSG_RUN                    := ==> Running bot from source...
  MSG_CLEAN                  := ==> Cleaning build artifacts...

  MSG_INSTALL_DONE_TITLE     := ✅ Deployment completed successfully!
  MSG_INSTALL_DONE_RUNNING   := Service $(BOT_NAME).service is running.
  MSG_INSTALL_DONE_ENV       := Remember to verify your bot token in: $(REPO_DIR)/.env

  MSG_STEP1_TITLE            := ==> [Step 1] Setting up environment and user $(DEPLOY_USER)...
  MSG_STEP1_USER_EXISTS      := User $(DEPLOY_USER) already exists.
  MSG_STEP1_USER_CREATED     := User $(DEPLOY_USER) created and added to sudoers.

  MSG_STEP2_TITLE            := ==> [Step 2] Verifying agy and running manager.py...
  MSG_STEP2_RESTORE_BAK      := Restoring agy from agy.bak...
  MSG_STEP2_RUN_MANAGER      := Running manager.py patch cli...
  MSG_STEP2_SKIP_PATCH       := Skipping patch (already patched or not required)

  MSG_STEP3_TITLE            := ==> [Step 3] Configuring systemd service $(BOT_NAME)...
  MSG_STEP3_CREATING         := Creating $(SERVICE_FILE)...
  MSG_STEP3_ALREADY_EXISTS   := Service $(BOT_NAME).service is already registered.

  MSG_STEP4_TITLE            := ==> [Step 4] Cloning $(REPO_URL), configuring .env, and building bot...
  MSG_STEP4_INSTALL_GO       := Installing Go 1.23...
  MSG_STEP4_CLONE_REPO       := Cloning repository...
  MSG_STEP4_UPDATE_REPO      := Repository already exists, updating branch...
  MSG_STEP4_COPY_ENV         := Copying .env.example to .env...
  MSG_STEP4_WARN_ENV         := Warning: .env.example not found, creating empty .env
  MSG_STEP4_KEEP_ENV         := File .env already exists, keeping existing configuration.
  MSG_STEP4_BUILD_BIN        := Compiling Go binary...

  MSG_STEP5_TITLE            := ==> [Step 5] Starting and verifying $(BOT_NAME).service...
  MSG_STEP5_ACTIVE           := ✅ Service is active (running)
  MSG_STEP5_FAILED           := ⚠️ Service failed to start (check settings in .env)
endif

.PHONY: all install step1-user step2-agy step3-service step4-clone-build step5-start help build test run clean

all: help

help:
	@echo "$(MSG_HELP_TITLE)"
	@echo ""
	@echo "  $(MSG_HELP_DEPLOY_SECTION)"
	@echo "    $(MSG_HELP_INSTALL)"
	@echo "    $(MSG_HELP_STEP1)"
	@echo "    $(MSG_HELP_STEP2)"
	@echo "    $(MSG_HELP_STEP3)"
	@echo "    $(MSG_HELP_STEP4)"
	@echo "    $(MSG_HELP_STEP5)"
	@echo ""
	@echo "  $(MSG_HELP_DEV_SECTION)"
	@echo "    $(MSG_HELP_BUILD)"
	@echo "    $(MSG_HELP_TEST)"
	@echo "    $(MSG_HELP_RUN)"
	@echo "    $(MSG_HELP_CLEAN)"
	@echo ""
	@echo "  $(MSG_HELP_LANG_SECTION)"
	@echo "    $(MSG_HELP_LANG_EN)"
	@echo "    $(MSG_HELP_LANG_RU)"

build:
	@echo "$(MSG_BUILD)"
	go build -o bot ./cmd/bot

test:
	@echo "$(MSG_TEST)"
	go test ./...

run:
	@echo "$(MSG_RUN)"
	go run ./cmd/bot

clean:
	@echo "$(MSG_CLEAN)"
	rm -f bot

install: step1-user step2-agy step3-service step4-clone-build step5-start
	@echo ""
	@echo "=================================================="
	@echo "$(MSG_INSTALL_DONE_TITLE)"
	@echo "$(MSG_INSTALL_DONE_RUNNING)"
	@echo "$(MSG_INSTALL_DONE_ENV)"
	@echo "=================================================="

# -------------------------------------------------------------
# Step 1 / Шаг 1: deploy user, packages, directories
# -------------------------------------------------------------
step1-user:
	@echo "$(MSG_STEP1_TITLE)"
	@apt-get update && apt-get install -y git curl wget build-essential sudo python3 python3-pip
	@if id "$(DEPLOY_USER)" &>/dev/null; then \
		echo "$(MSG_STEP1_USER_EXISTS)"; \
	else \
		useradd -m -s /bin/bash $(DEPLOY_USER); \
		echo "$(DEPLOY_USER) ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/90-$(DEPLOY_USER); \
		chmod 0440 /etc/sudoers.d/90-$(DEPLOY_USER); \
		echo "$(MSG_STEP1_USER_CREATED)"; \
	fi
	@mkdir -p $(BIN_DIR) /home/$(DEPLOY_USER)/projects
	@chown -R $(DEPLOY_USER):$(DEPLOY_USER) /home/$(DEPLOY_USER)

# -------------------------------------------------------------
# Step 2 / Шаг 2: agy verification and manager.py patching
# -------------------------------------------------------------
step2-agy:
	@echo "$(MSG_STEP2_TITLE)"
	@sudo -u $(DEPLOY_USER) -i bash -c '\
		mkdir -p ~/.local/bin; \
		if [ -f ~/.local/bin/agy.bak ]; then \
			echo "$(MSG_STEP2_RESTORE_BAK)"; \
			cp ~/.local/bin/agy.bak ~/.local/bin/agy; \
			chmod +x ~/.local/bin/agy; \
		fi; \
		if [ -f manager.py ]; then \
			echo "$(MSG_STEP2_RUN_MANAGER)"; \
			python3 manager.py --path-cli ~/.local/bin/agy patch cli || echo "$(MSG_STEP2_SKIP_PATCH)"; \
		fi \
	'

# -------------------------------------------------------------
# Step 3 / Шаг 3: systemd service registration
# -------------------------------------------------------------
step3-service:
	@echo "$(MSG_STEP3_TITLE)"
	@if [ ! -f $(SERVICE_FILE) ]; then \
		echo "$(MSG_STEP3_CREATING)"; \
		printf "[Unit]\nDescription=Bro Bot Agent Service\nAfter=network.target\n\n[Service]\nType=simple\nUser=$(DEPLOY_USER)\nWorkingDirectory=$(REPO_DIR)\nExecStart=$(REPO_DIR)/bot\nRestart=always\nRestartSec=5\nEnvironmentFile=-$(REPO_DIR)/.env\n\n[Install]\nWantedBy=multi-user.target\n" > $(SERVICE_FILE); \
		systemctl daemon-reload; \
		systemctl enable $(BOT_NAME).service; \
	else \
		echo "$(MSG_STEP3_ALREADY_EXISTS)"; \
	fi

# -------------------------------------------------------------
# Step 4 / Шаг 4: repository clone, .env configuration, Go build
# -------------------------------------------------------------
step4-clone-build:
	@echo "$(MSG_STEP4_TITLE)"
	@sudo -u $(DEPLOY_USER) -i bash -c '\
		set -e; \
		if ! command -v go &>/dev/null; then \
			echo "$(MSG_STEP4_INSTALL_GO)"; \
			wget -q https://go.dev/dl/go1.23.0.linux-amd64.tar.gz -O /tmp/go.tar.gz; \
			sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf /tmp/go.tar.gz; \
			rm /tmp/go.tar.gz; \
			echo "export PATH=\$$PATH:/usr/local/go:$$HOME/.local/bin" >> ~/.bashrc; \
		fi; \
		export PATH=$$PATH:/usr/local/go:$$HOME/.local/bin; \
		\
		if [ ! -d "$(REPO_DIR)/.git" ]; then \
			echo "$(MSG_STEP4_CLONE_REPO)"; \
			git clone $(REPO_URL) $(REPO_DIR); \
		else \
			echo "$(MSG_STEP4_UPDATE_REPO)"; \
			cd $(REPO_DIR) && git pull || true; \
		fi; \
		\
		cd $(REPO_DIR); \
		\
		if [ ! -f .env ]; then \
			if [ -f .env.example ]; then \
				echo "$(MSG_STEP4_COPY_ENV)"; \
				cp .env.example .env; \
			else \
				echo "$(MSG_STEP4_WARN_ENV)"; \
				touch .env; \
			fi; \
		else \
			echo "$(MSG_STEP4_KEEP_ENV)"; \
		fi; \
		\
		echo "$(MSG_STEP4_BUILD_BIN)"; \
		go build -o bot ./cmd/bot; \
	'

# -------------------------------------------------------------
# Step 5 / Шаг 5: service startup and status check
# -------------------------------------------------------------
step5-start:
	@echo "$(MSG_STEP5_TITLE)"
	@systemctl restart $(BOT_NAME).service
	@systemctl is-active --quiet $(BOT_NAME).service && echo "$(MSG_STEP5_ACTIVE)" || echo "$(MSG_STEP5_FAILED)"
	@systemctl status $(BOT_NAME).service --no-pager
