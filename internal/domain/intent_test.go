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

		// Разговор: сохранение данных, загрузка файлов и создание отдельных скриптов
		{"создать скрипт в scripts", "создай скрипт в папке scripts в проекте bro-bot", IntentChat},
		{"подгрузить данные по пути", "подгрузи данные в /home/deploy/pair-edge-datasets/moex_cache_1m/", IntentChat},
		{"скачать и сохранить csv", "скачай свечи по SBER и сохрани в csv на диск", IntentChat},
		{"написать python скрипт", "напиши python скрипт load_candles.py для выгрузки котировок", IntentChat},
		{"download data english", "download candles to /home/deploy/pair-edge-datasets/moex_cache_1m/", IntentChat},
		{"write script english", "write a python script to fetch moex candles", IntentChat},
		{"save script with exclusion word in path", "создай скрипт ~/scripts/deploy_test_helper.sh", IntentChat},
		{"save csv in tmp test path", "сохрани данные в /tmp/test_cache/out.csv", IntentChat},

		// Работа: инженерные задачи со словом файл/скрипт НЕ попадают в быструю запись
		{"починить скрипт", "почини скрипт scripts/load.py", IntentWork},
		{"рефакторинг скрипта", "отрефактори скрипт load_data.py", IntentWork},
		{"удалить скрипт", "удали скрипт old.sh", IntentWork},
		{"тесты для скрипта", "напиши тесты для scripts/load.py", IntentWork},

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
		{"подгрузи данные в /home/deploy/pair-edge-datasets/moex_cache_1m/", IntentReasonChatSave},
		{"создай скрипт в папке scripts в проекте bro-bot", IntentReasonChatSave},
	}

	for _, tt := range tests {
		_, reason := ClassifyMessageReason(tt.in)
		if reason != tt.wantReason {
			t.Errorf("ClassifyMessageReason(%q) вернул правило %q, ожидали %q", tt.in, reason, tt.wantReason)
		}
	}
}

func TestNormalizeAndStateWriteModes(t *testing.T) {
	var ps ProjectState
	if got := ps.GetChatWriteMode(); got != WriteModeAsk {
		t.Fatalf("default ChatWriteMode = %q, want %q", got, WriteModeAsk)
	}
	if got := ps.GetPlanWriteMode(); got != WriteModeDeny {
		t.Fatalf("default PlanWriteMode = %q, want %q", got, WriteModeDeny)
	}

	ps.SetChatWriteMode("allow")
	ps.SetPlanWriteMode("ask")
	if ps.GetChatWriteMode() != WriteModeAllow || ps.GetPlanWriteMode() != WriteModeAsk {
		t.Fatalf("unexpected modes: chat=%q plan=%q", ps.GetChatWriteMode(), ps.GetPlanWriteMode())
	}

	ps.SetChatWriteMode("off")
	ps.SetPlanWriteMode("approve")
	if ps.GetChatWriteMode() != WriteModeDeny || ps.GetPlanWriteMode() != WriteModeAllow {
		t.Fatalf("unexpected alias modes: chat=%q plan=%q", ps.GetChatWriteMode(), ps.GetPlanWriteMode())
	}
}

func TestIntentString(t *testing.T) {
	if IntentChat.String() != "chat" || IntentWork.String() != "work" {
		t.Errorf("неожиданные строковые представления: %s / %s", IntentChat, IntentWork)
	}
}
