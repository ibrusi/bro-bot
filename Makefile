SHELL := /bin/bash
DEPLOY_USER := deploy
BOT_NAME := bro-bot
REPO_URL := https://github.com/ibrusi/bro-bot.git
REPO_DIR := /home/$(DEPLOY_USER)/$(BOT_NAME)
BIN_DIR := /home/$(DEPLOY_USER)/.local/bin
SERVICE_FILE := /etc/systemd/system/$(BOT_NAME).service

.PHONY: all install step1-user step2-agy step3-service step4-clone-build step5-start help build test run clean

all: help

help:
	@echo "Доступные команды Makefile:"
	@echo ""
	@echo "  Развертывание на сервере (запускать под root на чистом сервере):"
	@echo "    sudo make install           - Полное развертывание (все 5 шагов)"
	@echo "    sudo make step1-user        - Шаг 1: Создание пользователя $(DEPLOY_USER), окружения и пакетов"
	@echo "    sudo make step2-agy         - Шаг 2: Проверка/откат agy и проверка сигнатур manager.py"
	@echo "    sudo make step3-service     - Шаг 3: Настройка и регистрация systemd-службы $(BOT_NAME)"
	@echo "    sudo make step4-clone-build - Шаг 4: Клонирование репозитория, настройка .env и сборка Go"
	@echo "    sudo make step5-start       - Шаг 5: Запуск и проверка статуса службы $(BOT_NAME).service"
	@echo ""
	@echo "  Разработка и сборка (для локальной работы):"
	@echo "    make build                  - Компиляция Go-бинарника (./cmd/bot)"
	@echo "    make test                   - Запуск всех тестов проекта"
	@echo "    make run                    - Запуск бота из исходников"
	@echo "    make clean                  - Удаление скомпилированного бинарника"

build:
	@echo "==> Компиляция Go-бинарника..."
	go build -o bot ./cmd/bot

test:
	@echo "==> Запуск тестов..."
	go test ./...

run:
	@echo "==> Запуск бота..."
	go run ./cmd/bot

clean:
	@echo "==> Очистка артефактов сборки..."
	rm -f bot

install: step1-user step2-agy step3-service step4-clone-build step5-start
	@echo ""
	@echo "=================================================="
	@echo "✅ Развертывание полностью завершено!"
	@echo "Служба $(BOT_NAME).service запущена."
	@echo "Не забудьте проверить токен бота в: $(REPO_DIR)/.env"
	@echo "=================================================="

# -------------------------------------------------------------
# Шаг 1: Создание пользователя deploy, окружения и пакетов
# -------------------------------------------------------------
step1-user:
	@echo "==> [Шаг 1] Настройка окружения и пользователя $(DEPLOY_USER)..."
	@apt-get update && apt-get install -y git curl wget build-essential sudo python3 python3-pip
	@if id "$(DEPLOY_USER)" &>/dev/null; then \
		echo "Пользователь $(DEPLOY_USER) уже существует."; \
	else \
		useradd -m -s /bin/bash $(DEPLOY_USER); \
		echo "$(DEPLOY_USER) ALL=(ALL) NOPASSWD:ALL" > /etc/sudoers.d/90-$(DEPLOY_USER); \
		chmod 0440 /etc/sudoers.d/90-$(DEPLOY_USER); \
		echo "Пользователь $(DEPLOY_USER) создан и добавлен в sudoers."; \
	fi
	@mkdir -p $(BIN_DIR) /home/$(DEPLOY_USER)/projects
	@chown -R $(DEPLOY_USER):$(DEPLOY_USER) /home/$(DEPLOY_USER)

# -------------------------------------------------------------
# Шаг 2: Проверка / откат agy и проверка сигнатур manager.py
# -------------------------------------------------------------
step2-agy:
	@echo "==> [Шаг 2] Проверка agy и запуск manager.py..."
	@sudo -u $(DEPLOY_USER) -i bash -c '\
		mkdir -p ~/.local/bin; \
		if [ -f ~/.local/bin/agy.bak ]; then \
			echo "Восстанавливаем agy из agy.bak..."; \
			cp ~/.local/bin/agy.bak ~/.local/bin/agy; \
			chmod +x ~/.local/bin/agy; \
		fi; \
		if [ -f manager.py ]; then \
			echo "Запуск manager.py patch cli..."; \
			python3 manager.py --path-cli ~/.local/bin/agy patch cli || echo "Пропуск патча (уже пропатчен или не требуется)"; \
		fi \
	'

# -------------------------------------------------------------
# Шаг 3: Создание и регистрация systemd-службы
# -------------------------------------------------------------
step3-service:
	@echo "==> [Шаг 3] Настройка systemd-сервиса $(BOT_NAME)..."
	@if [ ! -f $(SERVICE_FILE) ]; then \
		echo "Создаем $(SERVICE_FILE)..."; \
		printf "[Unit]\nDescription=Bro Bot Agent Service\nAfter=network.target\n\n[Service]\nType=simple\nUser=$(DEPLOY_USER)\nWorkingDirectory=$(REPO_DIR)\nExecStart=$(REPO_DIR)/bot\nRestart=always\nRestartSec=5\nEnvironmentFile=-$(REPO_DIR)/.env\n\n[Install]\nWantedBy=multi-user.target\n" > $(SERVICE_FILE); \
		systemctl daemon-reload; \
		systemctl enable $(BOT_NAME).service; \
	else \
		echo "Служба $(BOT_NAME).service уже зарегистрирована."; \
	fi

# -------------------------------------------------------------
# Шаг 4: Клонирование репозитория, настройка .env и сборка Go
# -------------------------------------------------------------
step4-clone-build:
	@echo "==> [Шаг 4] Клонирование $(REPO_URL), настройка .env и сборка бота..."
	@sudo -u $(DEPLOY_USER) -i bash -c '\
		set -e; \
		# Проверка и установка Go (если отсутствует) \
		if ! command -v go &>/dev/null; then \
			echo "Установка Go 1.23..."; \
			wget -q https://go.dev/dl/go1.23.0.linux-amd64.tar.gz -O /tmp/go.tar.gz; \
			sudo rm -rf /usr/local/go && sudo tar -C /usr/local -xzf /tmp/go.tar.gz; \
			rm /tmp/go.tar.gz; \
			echo "export PATH=\$$PATH:/usr/local/go:$$HOME/.local/bin" >> ~/.bashrc; \
		fi; \
		export PATH=$$PATH:/usr/local/go:$$HOME/.local/bin; \
		\
		# Клонирование или обновление репозитория \
		if [ ! -d "$(REPO_DIR)/.git" ]; then \
			echo "Клонируем репозиторий..."; \
			git clone $(REPO_URL) $(REPO_DIR); \
		else \
			echo "Репозиторий уже склонирован, обновляем ветку..."; \
			cd $(REPO_DIR) && git pull || true; \
		fi; \
		\
		cd $(REPO_DIR); \
		\
		# Подготовка .env из .env.example \
		if [ ! -f .env ]; then \
			if [ -f .env.example ]; then \
				echo "Копируем .env.example в .env..."; \
				cp .env.example .env; \
			else \
				echo "Предупреждение: .env.example не найден, создаем пустой .env"; \
				touch .env; \
			fi; \
		else \
			echo "Файл .env уже существует, сохраняем существующий."; \
		fi; \
		\
		# Сборка бинарника \
		echo "Компиляция Go-бинарника..."; \
		go build -o bot ./cmd/bot; \
	'

# -------------------------------------------------------------
# Шаг 5: Запуск и проверка статуса службы
# -------------------------------------------------------------
step5-start:
	@echo "==> [Шаг 5] Запуск и проверка службы $(BOT_NAME).service..."
	@systemctl restart $(BOT_NAME).service
	@systemctl is-active --quiet $(BOT_NAME).service && echo "✅ Служба активна (running)" || echo "⚠️ Служба не смогла запуститься (проверьте значения в .env)"
	@systemctl status $(BOT_NAME).service --no-pager
