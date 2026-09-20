package handlers

import (
	"errors"
	"strings"

	"bro-bot/internal/agents"
	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/utils"
)

// Слой представления для ошибок нижних слоёв.
//
// Домен, реестр агентов и проверки путей возвращают типизированные ошибки со
// служебным текстом на английском: он уходит в логи и сравнивается через
// errors.Is/errors.As. Пользователю показывается перевод, и собирается он здесь —
// единственном месте, которое знает и про язык интерфейса, и про каталог сообщений.

// ErrorText переводит известную ошибку на язык интерфейса. Незнакомая ошибка
// возвращается как есть: показать техническую строку лучше, чем промолчать.
func ErrorText(err error, lang string) string {
	if err == nil {
		return ""
	}

	var taskNotFound *domain.TaskNotFoundError
	if errors.As(err, &taskNotFound) {
		return i18n.Tf(lang, "err.task_not_found", taskNotFound.ID)
	}

	var taskFinished *domain.TaskAlreadyFinishedError
	if errors.As(err, &taskFinished) {
		return i18n.Tf(lang, "err.task_already", taskFinished.ID,
			i18n.TaskStatusTitle(string(taskFinished.Status), lang))
	}

	var unknownAgent *agents.UnknownAgentError
	if errors.As(err, &unknownAgent) {
		return i18n.Tf(lang, "err.unknown_agent", unknownAgent.Name, strings.Join(unknownAgent.Known, ", "))
	}

	var missingKey *agents.MissingAPIKeyError
	if errors.As(err, &missingKey) {
		return i18n.Tf(lang, "err.missing_api_key", missingKey.Agent, strings.Join(missingKey.Vars, " / "))
	}

	var unsupportedMode *agents.UnsupportedModeError
	if errors.As(err, &unsupportedMode) {
		key := "err.agent_no_cli"
		if unsupportedMode.Mode == agents.ModeAPI {
			key = "err.agent_no_api"
		}
		return i18n.Tf(lang, key, unsupportedMode.Agent)
	}

	if text, ok := pathErrorText(err, lang); ok {
		return text
	}
	if text, ok := repoErrorText(err, lang); ok {
		return text
	}

	return err.Error()
}

// pathErrorText переводит ошибки проверки имени проекта, скрипта и пути.
func pathErrorText(err error, lang string) (string, bool) {
	switch {
	case errors.Is(err, utils.ErrNameEmpty):
		return i18n.T(lang, "err.name_empty"), true
	case errors.Is(err, utils.ErrNameControlChar):
		return i18n.T(lang, "err.name_control_char"), true
	case errors.Is(err, utils.ErrNameLeadingDash):
		return i18n.T(lang, "err.name_leading_dash"), true
	case errors.Is(err, utils.ErrNameReserved):
		return i18n.T(lang, "err.name_reserved"), true
	case errors.Is(err, utils.ErrNameSeparators):
		return i18n.T(lang, "err.name_separators"), true
	case errors.Is(err, utils.ErrNameCharset):
		return i18n.T(lang, "err.name_charset"), true
	case errors.Is(err, utils.ErrRootMissing):
		return i18n.T(lang, "err.root_missing"), true
	case errors.Is(err, utils.ErrPathAbsolute):
		return i18n.T(lang, "err.path_absolute"), true
	case errors.Is(err, utils.ErrPathEscape):
		return i18n.T(lang, "err.path_escape"), true
	}
	return "", false
}

// repoErrorText переводит ошибки разбора ссылки на репозиторий в команде /clone.
func repoErrorText(err error, lang string) (string, bool) {
	switch {
	case errors.Is(err, errRepoURLEmpty):
		return i18n.T(lang, "err.repo_url_empty"), true
	case errors.Is(err, errRepoURLInvalid):
		return i18n.T(lang, "err.repo_url_invalid"), true
	case errors.Is(err, errRepoURLProtocol):
		return i18n.T(lang, "err.repo_url_protocol"), true
	case errors.Is(err, errRepoNameUndetected):
		return i18n.T(lang, "err.repo_name_undetected"), true
	case errors.Is(err, errRepoNameInvalid):
		return i18n.T(lang, "err.repo_name_invalid"), true
	}
	return "", false
}

// uiLang — язык интерфейса для текущего ответа бота.
func uiLang() string {
	return config.ProjectState.GetLanguage()
}
