package domain

import "testing"

func TestClassifyMessage(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want Intent
	}{
		// Разговор: вопросы о проекте
		{"вопрос о роутере", "как работает роутер OnText?", IntentChat},
		{"вопрос без знака", "почему задача завершается сразу", IntentChat},
		{"вопрос про модель", "а какая ты модель?", IntentChat},
		{"объясни", "объясни архитектуру адаптеров", IntentChat},
		{"расскажи", "расскажи про создание задач в боте", IntentChat},
		{"покажи", "покажи, где регистрируются команды", IntentChat},
		{"найди", "найди места, где используется conversation_id", IntentChat},
		{"сравни", "сравни agy и claude по скорости", IntentChat},
		{"вопрос с рабочим глаголом", "как добавить кэш?", IntentChat},
		{"вопрос стоит ли", "стоит ли выносить это в отдельный пакет", IntentChat},

		// Разговор: короткие реплики
		{"приветствие", "привет", IntentChat},
		{"благодарность", "спасибо, всё понятно", IntentChat},
		{"подтверждение", "ок", IntentChat},
		{"проверка связи", "Супер, я просто проверял связь, извини, что побеспокоил)", IntentChat},
		{"пустое", "   ", IntentChat},

		// Работа: повелительная форма в начале
		{"сделай рефакторинг", "сделай рефакторинг handlers", IntentWork},
		{"исправь тест", "исправь падающий тест в models", IntentWork},
		{"почини", "почини сборку", IntentWork},
		{"добавь команду", "пожалуйста добавь команду /chat", IntentWork},
		{"нужно добавить", "нужно добавить таймаут для чата", IntentWork},
		{"можешь реализовать", "можешь реализовать разговорный режим", IntentWork},
		{"напиши тесты", "напиши тесты для классификатора", IntentWork},
		{"удали файл", "удали неиспользуемый файл utils/old.go", IntentWork},
		{"обнови зависимости", "обнови зависимости в go.mod", IntentWork},
		{"вынеси функцию", "вынеси выбор адаптера в отдельную функцию", IntentWork},
		{"fix english", "fix the flaky test", IntentWork},
		{"implement english", "implement chat mode", IntentWork},
		{"refactor english", "refactor the handlers package", IntentWork},
		{"please add english", "please add a /chat command", IntentWork},

		// Работа: рабочий глагол в середине сообщения
		{"проверь и поправь", "Проверь и поправь, пожалуйста", IntentWork},
		{"после запятой", "тут баг в резолве моделей, исправь его", IntentWork},
		{"не мог бы ты", "не мог бы ты добавить кэш моделей", IntentWork},

		// Работа: упоминание git-операций
		{"создай ветку", "создай ветку и залей изменения", IntentWork},
		{"открой pr", "открой pr после проверки", IntentWork},
		{"задеплой", "задеплой на прод", IntentWork},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, reason := ClassifyMessageReason(tt.in)
			if got != tt.want {
				t.Errorf("ClassifyMessage(%q) = %v (правило %s), ожидали %v", tt.in, got, reason, tt.want)
			}
		})
	}
}

func TestClassifyMessageReason(t *testing.T) {
	tests := []struct {
		in         string
		wantReason string
	}{
		{"", IntentReasonEmpty},
		{"создай ветку feat/chat", IntentReasonVCS},
		{"добавь тесты", IntentReasonImperative},
		{"как это работает?", IntentReasonQuestion},
		{"объясни поток событий", IntentReasonReadOnlyVerb},
		{"тут всё сломалось, почини", IntentReasonWorkMention},
		{"интересная штука получилась", IntentReasonDefaultChat},
	}

	for _, tt := range tests {
		_, reason := ClassifyMessageReason(tt.in)
		if reason != tt.wantReason {
			t.Errorf("ClassifyMessageReason(%q) вернул правило %q, ожидали %q", tt.in, reason, tt.wantReason)
		}
	}
}

func TestIntentString(t *testing.T) {
	if IntentChat.String() != "chat" || IntentWork.String() != "work" {
		t.Errorf("неожиданные строковые представления: %s / %s", IntentChat, IntentWork)
	}
}
