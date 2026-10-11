package models

import (
	"bro-bot/internal/effort"
	"reflect"
	"testing"
)

// agyCatalog — вывод `agy models` версии 1.3.3.
var agyCatalog = []string{
	"gemini-3.8-flash-high", "gemini-3.8-flash-medium", "gemini-3.8-flash-low",
	"gemini-3.1-pro-high", "gemini-3.1-pro-low",
	"claude-opus-5-5-low", "claude-opus-5-5-medium", "claude-opus-5-5-high",
	"claude-sonnet-5-5-low", "claude-sonnet-5-5-medium", "claude-sonnet-5-5-high",
	"gpt-oss-120b-medium",
}

func TestBuildAgyModelArgs(t *testing.T) {
	tests := []struct {
		name     string
		model    string
		level    effort.Level
		expected []string
	}{
		// Базовое имя семейства: agy без уровня его не запустит.
		{"base claude auto", "claude-opus-5-5", effort.Auto, []string{"--model", "claude-opus-5-5-medium"}},
		{"base claude high", "claude-opus-5-5", effort.High, []string{"--model", "claude-opus-5-5-high"}},
		{"base claude max clamps", "claude-opus-5-5", effort.Max, []string{"--model", "claude-opus-5-5-high"}},
		{"base pro auto", "gemini-3.1-pro", effort.Auto, []string{"--model", "gemini-3.1-pro-high"}},
		{"base flash auto", "gemini-3.8-flash", effort.Auto, []string{"--model", "gemini-3.8-flash-medium"}},

		// Вариант с уровнем в имени: при auto остаётся как есть.
		{"variant auto", "claude-opus-5-5-high", effort.Auto, []string{"--model", "claude-opus-5-5-high"}},
		{"single variant auto", "gpt-oss-120b-medium", effort.Auto, []string{"--model", "gpt-oss-120b-medium"}},

		// Явный уровень выбирает другой вариант, а не конфликтующий --effort.
		{"variant override", "gemini-3.1-pro-high", effort.Low, []string{"--model", "gemini-3.1-pro-low"}},
		{"variant same level", "gemini-3.1-pro-high", effort.High, []string{"--model", "gemini-3.1-pro-high"}},
		{"missing level rounds down", "gemini-3.1-pro-high", effort.Medium, []string{"--model", "gemini-3.1-pro-low"}},
		{"xhigh clamps to high", "gemini-3.8-flash-low", effort.XHigh, []string{"--model", "gemini-3.8-flash-high"}},
		{"below floor takes lowest", "gpt-oss-120b-medium", effort.Low, []string{"--model", "gpt-oss-120b-medium"}},
		{"above ceiling takes highest", "gpt-oss-120b-medium", effort.Max, []string{"--model", "gpt-oss-120b-medium"}},

		// Модели вне каталога — прежние эвристики.
		{"legacy claude", "claude-sonnet-4-6", effort.High, []string{"--model", "claude-sonnet-4-6"}},
		{"legacy thinking", "claude-opus-4-6-thinking", effort.Auto, []string{"--model", "claude-opus-4-6-thinking"}},
		{"legacy variant keeps own level", "gemini-9-flash-low", effort.Max, []string{"--model", "gemini-9-flash-low"}},
		{"legacy pro default", "gemini-9-pro", effort.Auto, []string{"--model", "gemini-9-pro", "--effort", "high"}},
		{"legacy gemini default", "gemini-9-flash", effort.Auto, []string{"--model", "gemini-9-flash", "--effort", "medium"}},
		{"legacy explicit", "my-custom-model", effort.Medium, []string{"--model", "my-custom-model", "--effort", "medium"}},
		{"legacy custom auto", "my-custom-model", effort.Auto, []string{"--model", "my-custom-model"}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			actual := BuildAgyModelArgs(tc.model, tc.level, agyCatalog)
			if !reflect.DeepEqual(actual, tc.expected) {
				t.Errorf("BuildAgyModelArgs(%q, %q) = %v, expected %v", tc.model, tc.level, actual, tc.expected)
			}
		})
	}
}

// Без каталога базовое имя уходит в agy как есть — так же, как до появления семейств.
func TestBuildAgyModelArgsEmptyCatalog(t *testing.T) {
	actual := BuildAgyModelArgs("gemini-3.1-pro", effort.Auto, nil)
	expected := []string{"--model", "gemini-3.1-pro", "--effort", "high"}
	if !reflect.DeepEqual(actual, expected) {
		t.Errorf("BuildAgyModelArgs без каталога = %v, expected %v", actual, expected)
	}
}

func TestSplitEffortSuffix(t *testing.T) {
	tests := []struct {
		id    string
		base  string
		level effort.Level
		ok    bool
	}{
		{"claude-opus-5-5-high", "claude-opus-5-5", effort.High, true},
		{"gemini-3.8-flash-medium", "gemini-3.8-flash", effort.Medium, true},
		{"some-model-xhigh", "some-model", effort.XHigh, true},
		{"some-model-max", "some-model", effort.Max, true},
		{"claude-opus-5-5", "claude-opus-5-5", effort.Auto, false},
		{"claude-opus-4-6-thinking", "claude-opus-4-6-thinking", effort.Auto, false},
		{"-high", "-high", effort.Auto, false},
	}
	for _, tc := range tests {
		base, level, ok := splitEffortSuffix(tc.id)
		if base != tc.base || level != tc.level || ok != tc.ok {
			t.Errorf("splitEffortSuffix(%q) = (%q, %q, %v), expected (%q, %q, %v)",
				tc.id, base, level, ok, tc.base, tc.level, tc.ok)
		}
	}
}

func TestGroupEffortFamiliesSortsAndDedupes(t *testing.T) {
	families := groupEffortFamilies([]string{
		"m-high", "m-low", "m-high", "m-medium", "standalone", "claude-opus-4-6-thinking",
	})
	expected := effortFamilies{"m": {effort.Low, effort.Medium, effort.High}}
	if !reflect.DeepEqual(families, expected) {
		t.Errorf("groupEffortFamilies = %v, expected %v", families, expected)
	}
}

// До создания реестра каталог берётся из встроенного списка.
func TestKnownModelIDsFallsBackWithoutRegistry(t *testing.T) {
	prev := GlobalModelRegistry
	GlobalModelRegistry = nil
	t.Cleanup(func() { GlobalModelRegistry = prev })

	ids := KnownModelIDs()
	if len(ids) != len(fallbackModels) {
		t.Fatalf("KnownModelIDs() вернул %d моделей, ожидали %d", len(ids), len(fallbackModels))
	}
	for i, item := range fallbackModels {
		if ids[i] != item.ID {
			t.Errorf("KnownModelIDs()[%d] = %q, expected %q", i, ids[i], item.ID)
		}
	}
}
