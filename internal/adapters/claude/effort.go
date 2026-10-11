package claude

import (
	"encoding/json"
	"strings"

	"bro-bot/internal/effort"
)

// claudeEffortSupport — поддержка уровней усилия моделью по capabilities.effort
// из /v1/models. Поле устроено так:
//
//	"effort": {"supported": true, "low": {"supported": true}, ..., "max": {"supported": true}}
//
// Нулевое значение означает, что API о поддержке ничего не сообщил.
type claudeEffortSupport struct {
	// Known — в ответе API было поле capabilities.effort.
	Known bool
	// Levels — уровни, которые модель принимает в output_config.effort.
	Levels map[effort.Level]bool
}

type claudeSupportedFlag struct {
	Supported bool `json:"supported"`
}

// UnmarshalJSON разбирает capabilities.effort. Уровни, которых бот не знает,
// пропускаются: новая ступень шкалы на стороне API не должна ломать список моделей.
func (s *claudeEffortSupport) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	var supported bool
	if value, ok := raw["supported"]; ok {
		if err := json.Unmarshal(value, &supported); err != nil {
			return err
		}
	}

	levels := make(map[effort.Level]bool, len(raw))
	if supported {
		for key, value := range raw {
			level, known := effort.Parse(key)
			if !known || level.IsAuto() {
				continue
			}
			var flag claudeSupportedFlag
			if err := json.Unmarshal(value, &flag); err != nil {
				return err
			}
			if flag.Supported {
				levels[level] = true
			}
		}
	}

	*s = claudeEffortSupport{Known: true, Levels: levels}
	return nil
}

// supports сообщает, что модель точно принимает уровень level.
func (s claudeEffortSupport) supports(level effort.Level) bool {
	return s.Known && s.Levels[level]
}

// claudeEffortSupported сообщает, что уровень level нужно передать модели
// modelName в output_config.effort.
//
// Уровень передаётся только тогда, когда список моделей подтверждает его
// поддержку: Haiku 4.5 и Sonnet 4.5 не принимают effort вовсе, а xhigh появился
// лишь с Opus 4.7. Запрос с неподдерживаемым уровнем API отклоняет с ошибкой 400,
// поэтому без подтверждения уровень не отправляется и модель работает со своим
// уровнем по умолчанию.
func claudeEffortSupported(modelName string, level effort.Level) bool {
	if level.IsAuto() {
		return false
	}
	for _, m := range modelCache.cached() {
		if strings.EqualFold(m.ID, modelName) {
			return m.Effort.supports(level)
		}
	}
	return false
}

// applyClaudeEffort выставляет output_config.effort в теле запроса к модели
// modelName либо убирает его, если модель этот уровень не подтверждает.
func applyClaudeEffort(body map[string]interface{}, modelName string, level effort.Level) {
	if claudeEffortSupported(modelName, level) {
		body["output_config"] = map[string]interface{}{"effort": level.String()}
		return
	}
	delete(body, "output_config")
}
