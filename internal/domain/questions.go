package domain

import (
	"bro-bot/internal/i18n"
	"bro-bot/internal/storage"
	"fmt"
	"sort"
	"strings"
)

// QuestionItem — один структурированный вопрос агента пользователю.
type QuestionItem = storage.QuestionItem

// CloneQuestions делает глубокую копию слайса вопросов для безопасной передачи в TaskView.
func CloneQuestions(src []QuestionItem) []QuestionItem {
	if len(src) == 0 {
		return nil
	}
	dst := make([]QuestionItem, len(src))
	for i, q := range src {
		dst[i] = QuestionItem{
			Question:        q.Question,
			Options:         append([]string(nil), q.Options...),
			MultiSelect:     q.MultiSelect,
			SelectedIndices: append([]int(nil), q.SelectedIndices...),
			Answer:          q.Answer,
		}
	}
	return dst
}

// ToggleQuestionOption переключает индекс варианта в SelectedIndices и поддерживает сортировку.
func ToggleQuestionOption(q *QuestionItem, optIdx int) bool {
	for i, idx := range q.SelectedIndices {
		if idx == optIdx {
			q.SelectedIndices = append(q.SelectedIndices[:i], q.SelectedIndices[i+1:]...)
			return false
		}
	}
	q.SelectedIndices = append(q.SelectedIndices, optIdx)
	sort.Ints(q.SelectedIndices)
	return true
}

// FormatSelectedOptions возвращает выбранные варианты через запятую в порядке их номеров.
func FormatSelectedOptions(q QuestionItem) string {
	var chosen []string
	for _, idx := range q.SelectedIndices {
		if idx >= 0 && idx < len(q.Options) {
			chosen = append(chosen, q.Options[idx])
		}
	}
	return strings.Join(chosen, ", ")
}

// FormatCombinedAnswers собирает ответы на все вопросы в единое сообщение агенту.
// Для одиночного вопроса возвращает чистый текст ответа для обратной совместимости.
func FormatCombinedAnswers(questions []QuestionItem, lang string) string {
	if len(questions) == 0 {
		return ""
	}
	if len(questions) == 1 {
		return strings.TrimSpace(questions[0].Answer)
	}
	var b strings.Builder
	b.WriteString(i18n.T(lang, "pipeline.multi_answer_header"))
	for i, q := range questions {
		ans := strings.TrimSpace(q.Answer)
		if ans == "" {
			ans = i18n.T(lang, "pipeline.multi_answer_skipped")
		}
		b.WriteString(fmt.Sprintf("%d. %s\n   %s: %s\n", i+1, q.Question, i18n.T(lang, "pipeline.multi_answer_label"), ans))
	}
	return strings.TrimSpace(b.String())
}
