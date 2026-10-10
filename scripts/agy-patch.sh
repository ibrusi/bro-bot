#!/usr/bin/env bash
# Применяет патч agy-manager к указанным целям и печатает вывод команды.
# Цели: cli, manager, ide (можно несколько); без аргументов — cli.
# Запуск из бота: /script agy-patch.sh [цели...], например /script agy-patch.sh ide
# Путь к agy-manager можно переопределить переменной окружения AGY_MANAGER.
set -euo pipefail

AGY_MANAGER="${AGY_MANAGER:-/home/deploy/agy-manager}"

if [[ ! -x "$AGY_MANAGER" ]]; then
	echo "agy-manager не найден или не исполняемый: $AGY_MANAGER" >&2
	exit 1
fi

# stdin закрыт: без аргументов agy-manager открывает интерактивное меню, и
# любой его вопрос должен завершаться сразу, а не ждать ввода.
exec "$AGY_MANAGER" patch "${@:-cli}" </dev/null
