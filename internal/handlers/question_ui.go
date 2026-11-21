package handlers

import (
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
	"bro-bot/internal/utils"
	"fmt"
	"html"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	// maxShortOptionLabelRunes — предел длины одного варианта (в рунах),
	// при котором его полный текст гарантированно не обрезается в Telegram на смартфоне
	// в одноколоночной кнопке ("1. " + 18 = 21 симв., "⬜ 1. " + 18 = 23 симв.).
	maxShortOptionLabelRunes = 18

	// Пороги для компактной 2-колоночной раскладки коротких текстовых кнопок.
	maxTwoColSingleOptionRunes = 10
	maxTwoColMultiOptionRunes  = 8

	// Максимум компактных цифровых кнопок в одном ряду ([ 1 ]..[ 5 ] / [ ⬜ 1 ]..[ ☑️ 4 ]).
	maxSingleDigitButtonsPerRow = 5
	maxMultiDigitButtonsPerRow  = 4

	// Пороги для коротких кнопок альтернативных вариантов плана ("✅ " + 18 = 20 симв.).
	maxShortPlanVariantRunes  = 18
	maxTwoColPlanVariantRunes = 10
)

// canFitAllOptionsOnButtons проверяет, что каждый вариант однострочный и не превышает maxRunes.
func canFitAllOptionsOnButtons(options []string, maxRunes int) bool {
	if len(options) == 0 {
		return false
	}
	for _, opt := range options {
		trimmed := strings.TrimSpace(opt)
		if trimmed == "" || strings.ContainsAny(trimmed, "\n\r") {
			return false
		}
		if utf8.RuneCountInString(trimmed) > maxRunes {
			return false
		}
	}
	return true
}

// formatOptionTextHTML экранирует вариант под Telegram HTML и аккуратно выравнивает переносы строк.
func formatOptionTextHTML(opt string) string {
	cleaned := strings.ReplaceAll(strings.TrimSpace(opt), "\r\n", "\n")
	cleaned = strings.ReplaceAll(cleaned, "\r", "\n")
	escaped := html.EscapeString(cleaned)
	return strings.ReplaceAll(escaped, "\n", "\n   ")
}

// formatQuestionOptionsHTML строит полный нумерованный список вариантов для тела сообщения.
// В режиме мультивыбора перед номером ставится синхронный статус чекбокса (⬜ / ☑️).
func formatQuestionOptionsHTML(options []string, isMultiSelect bool, selectedSet map[int]bool) string {
	var b strings.Builder
	for i, opt := range options {
		if i > 0 {
			b.WriteByte('\n')
		}
		optHTML := formatOptionTextHTML(opt)
		if isMultiSelect {
			mark := "⬜ "
			if selectedSet[i] {
				mark = "☑️ "
			}
			b.WriteString(fmt.Sprintf("%s<b>%d.</b> %s", mark, i+1, optHTML))
		} else {
			b.WriteString(fmt.Sprintf("<b>%d.</b> %s", i+1, optHTML))
		}
	}
	return b.String()
}

// chunkButtonsBalanced раскладывает кнопки по максимально равномерным рядам
// не длиннее maxPerRow (например, 6 кнопок при maxPerRow=5 -> 3+3, а не 5+1).
func chunkButtonsBalanced(buttons []ports.Button, maxPerRow int) [][]ports.Button {
	n := len(buttons)
	if n == 0 {
		return nil
	}
	if maxPerRow <= 1 {
		rows := make([][]ports.Button, 0, n)
		for _, b := range buttons {
			rows = append(rows, []ports.Button{b})
		}
		return rows
	}
	numRows := (n + maxPerRow - 1) / maxPerRow
	base := n / numRows
	rem := n % numRows

	rows := make([][]ports.Button, 0, numRows)
	offset := 0
	for r := 0; r < numRows; r++ {
		size := base
		if r < rem {
			size++
		}
		rows = append(rows, buttons[offset:offset+size])
		offset += size
	}
	return rows
}

// layoutOptionButtons раскладывает кнопки вариантов по рядам: короткие — в 2 или 1 колонку,
// длинные или многострочные — в компактные сбалансированные ряды цифр.
func layoutOptionButtons(buttons []ports.Button, options []string, useFullLabels bool, twoColMax, digitMaxPerRow int) [][]ports.Button {
	if !useFullLabels {
		return chunkButtonsBalanced(buttons, digitMaxPerRow)
	}
	if len(buttons) > 1 && canFitAllOptionsOnButtons(options, twoColMax) {
		return chunkButtonsBalanced(buttons, 2)
	}
	return chunkButtonsBalanced(buttons, 1)
}

func makeSelectedSet(indices []int, optCount int) map[int]bool {
	set := make(map[int]bool, len(indices))
	for _, idx := range indices {
		if idx >= 0 && idx < optCount {
			set[idx] = true
		}
	}
	return set
}

// formatTaskQuestionMessage формирует сообщение с вопросом агента и полным нумерованным списком вариантов.
func formatTaskQuestionMessage(view domain.TaskView, isPlanning bool, lang string) string {
	totalQ := len(view.PendingQuestions)
	projHTML := html.EscapeString(view.Project)

	if totalQ <= 1 {
		rawQ := view.LastQuestion
		options := view.QuestionOptions
		var isMultiSelect bool
		var selectedSet map[int]bool
		if totalQ == 1 {
			q := view.PendingQuestions[0]
			rawQ = q.Question
			options = q.Options
			isMultiSelect = q.MultiSelect
			if isMultiSelect {
				selectedSet = makeSelectedSet(q.SelectedIndices, len(options))
			}
		}
		body := utils.MarkdownToTelegramHTML(rawQ)
		if len(options) > 0 {
			body += "\n\n" + formatQuestionOptionsHTML(options, isMultiSelect, selectedSet)
		}
		if isMultiSelect {
			body += i18n.Tf(lang, "task.multi_select_hint", html.EscapeString(i18n.T(lang, "btn.q_submit")))
		}
		headerKey := "pipeline.question_header"
		if isPlanning {
			headerKey = "pipeline.question_header_planning"
		}
		return i18n.Tf(lang, headerKey, view.ID, projHTML, body)
	}

	if view.QuestionAcceptAllTxt {
		var listBldr strings.Builder
		for i, q := range view.PendingQuestions {
			if i > 0 {
				listBldr.WriteString("\n\n")
			}
			listBldr.WriteString(fmt.Sprintf("<b>%d.</b> %s", i+1, utils.MarkdownToTelegramHTML(q.Question)))
			for j, opt := range q.Options {
				listBldr.WriteString(fmt.Sprintf("\n   %d) %s", j+1, formatOptionTextHTML(opt)))
			}
		}
		return i18n.Tf(lang, "task.multi_question_all_text_prompt", view.ID, projHTML, totalQ, listBldr.String())
	}

	idx := view.CurrentQuestionIndex
	if idx < 0 || idx >= totalQ {
		idx = 0
	}
	curQ := view.PendingQuestions[idx]

	var bodyBldr strings.Builder
	bodyBldr.WriteString(i18n.Tf(lang, "task.multi_question_progress", idx+1, totalQ, utils.MarkdownToTelegramHTML(curQ.Question)))
	if len(curQ.Options) > 0 {
		var selectedSet map[int]bool
		if curQ.MultiSelect {
			selectedSet = makeSelectedSet(curQ.SelectedIndices, len(curQ.Options))
		}
		bodyBldr.WriteString("\n\n")
		bodyBldr.WriteString(formatQuestionOptionsHTML(curQ.Options, curQ.MultiSelect, selectedSet))
	}
	if curQ.MultiSelect {
		bodyBldr.WriteString(i18n.Tf(lang, "task.multi_select_hint", html.EscapeString(i18n.T(lang, "btn.q_submit"))))
	}

	bodyBldr.WriteString(i18n.Tf(lang, "task.multi_question_list_header", totalQ))
	answeredMark := i18n.T(lang, "task.multi_question_answered_mark")
	currentMark := i18n.T(lang, "task.multi_question_current_mark")
	pendingMark := i18n.T(lang, "task.multi_question_pending_mark")
	for i, q := range view.PendingQuestions {
		mark := pendingMark
		if strings.TrimSpace(q.Answer) != "" {
			mark = answeredMark
		} else if i == idx {
			mark = currentMark
		}
		ansSuffix := ""
		if strings.TrimSpace(q.Answer) != "" {
			ansSuffix = i18n.Tf(lang, "task.multi_question_answer_inline", html.EscapeString(utils.TruncateString(q.Answer, 60)))
		}
		bodyBldr.WriteString(fmt.Sprintf("%s %d. %s%s\n",
			mark, i+1, html.EscapeString(utils.TruncateString(q.Question, 80)), ansSuffix))
	}

	headerKey := "task.question_header_multi"
	if isPlanning {
		headerKey = "task.question_header_planning_multi"
	}
	return i18n.Tf(lang, headerKey, view.ID, projHTML, idx+1, totalQ, strings.TrimSpace(bodyBldr.String()))
}

// buildQuestionMarkup строит адаптивную клавиатуру ответа: полные подписи для коротких вариантов
// и сбалансированный ряд номеров для длинных/многострочных вариантов.
func buildQuestionMarkup(task *domain.TaskSession, lang string) *ports.Keyboard {
	view15 := task.Snapshot()
	taskID := view15.ID
	totalQ := len(view15.PendingQuestions)
	qIdx := view15.CurrentQuestionIndex

	var (
		options       []string
		isMultiSelect bool
		selectedSet   map[int]bool
	)
	if totalQ > 0 {
		if qIdx < 0 || qIdx >= totalQ {
			qIdx = 0
		}
		q := view15.PendingQuestions[qIdx]
		options = append([]string(nil), q.Options...)
		isMultiSelect = q.MultiSelect
		if isMultiSelect {
			selectedSet = makeSelectedSet(q.SelectedIndices, len(options))
		}
	} else {
		qIdx = 0
		options = append([]string(nil), view15.QuestionOptions...)
	}

	var rows [][]ports.Button

	if !view15.QuestionAcceptAllTxt && len(options) > 0 {
		useFullLabels := canFitAllOptionsOnButtons(options, maxShortOptionLabelRunes)
		optButtons := make([]ports.Button, 0, len(options))
		for i, opt := range options {
			cleanOpt := strings.TrimSpace(opt)
			if isMultiSelect {
				prefix := "⬜ "
				if selectedSet[i] {
					prefix = "☑️ "
				}
				var btnText string
				if useFullLabels {
					btnText = fmt.Sprintf("%s%d. %s", prefix, i+1, cleanOpt)
				} else {
					btnText = fmt.Sprintf("%s%d", prefix, i+1)
				}
				optButtons = append(optButtons, ports.Button{
					Text:    btnText,
					Action:  "q_toggle",
					Payload: fmt.Sprintf("%d:%d:%d", taskID, qIdx, i),
				})
			} else {
				var btnText string
				if useFullLabels {
					btnText = fmt.Sprintf("%d. %s", i+1, cleanOpt)
				} else {
					btnText = strconv.Itoa(i + 1)
				}
				optButtons = append(optButtons, ports.Button{
					Text:    btnText,
					Action:  "q_choice",
					Payload: fmt.Sprintf("%d:%d:%d", taskID, qIdx, i),
				})
			}
		}

		twoColMax := maxTwoColSingleOptionRunes
		perRow := maxSingleDigitButtonsPerRow
		if isMultiSelect {
			twoColMax = maxTwoColMultiOptionRunes
			perRow = maxMultiDigitButtonsPerRow
		}
		rows = append(rows, layoutOptionButtons(optButtons, options, useFullLabels, twoColMax, perRow)...)

		if isMultiSelect {
			submitText := i18n.T(lang, "btn.q_submit")
			if len(selectedSet) > 0 {
				submitText = i18n.Tf(lang, "btn.q_submit_count", len(selectedSet))
			}
			rows = append(rows, []ports.Button{
				{Text: submitText, Action: "q_submit", Payload: fmt.Sprintf("%d:%d", taskID, qIdx)},
			})
		}
	}

	if totalQ > 1 {
		var nav []ports.Button
		if view15.QuestionAcceptAllTxt || qIdx > 0 {
			nav = append(nav, ports.Button{Text: i18n.T(lang, "btn.q_back"), Action: "q_back", Payload: strconv.Itoa(taskID)})
		}
		if !view15.QuestionAcceptAllTxt {
			nav = append(nav, ports.Button{Text: i18n.T(lang, "btn.q_all_text"), Action: "q_all_text", Payload: strconv.Itoa(taskID)})
		}
		if len(nav) > 0 {
			rows = append(rows, nav)
		}
	}

	rows = append(rows, []ports.Button{
		{Text: i18n.T(lang, "btn.pause"), Action: "q_pause", Payload: strconv.Itoa(taskID)},
		{Text: i18n.T(lang, "btn.cancel"), Action: "plan_cancel", Payload: strconv.Itoa(taskID)},
	})

	return &ports.Keyboard{Rows: rows}
}

// formatPlanVariantsSection форматирует нумерованный список альтернативных вариантов плана для тела сообщения.
func formatPlanVariantsSection(variants []string, lang string) string {
	if len(variants) == 0 {
		return ""
	}
	return i18n.T(lang, "plan.variants_header") + formatQuestionOptionsHTML(variants, false, nil)
}

// buildPlanApprovalMarkup строит клавиатуру утверждения плана с адаптивными кнопками выбора варианта.
func buildPlanApprovalMarkup(taskID int, variants []string, lang string) *ports.Keyboard {
	var rows [][]ports.Button

	if len(variants) > 0 {
		useFullLabels := canFitAllOptionsOnButtons(variants, maxShortPlanVariantRunes)
		varButtons := make([]ports.Button, 0, len(variants))
		for i, v := range variants {
			var btnText string
			if useFullLabels {
				btnText = i18n.Tf(lang, "btn.approve_variant", strings.TrimSpace(v))
			} else {
				btnText = i18n.Tf(lang, "btn.approve_variant_num", i+1)
			}
			varButtons = append(varButtons, ports.Button{
				Text:    btnText,
				Action:  "plan_appr_var",
				Payload: fmt.Sprintf("%d:%d", taskID, i),
			})
		}

		rows = append(rows, layoutOptionButtons(varButtons, variants, useFullLabels, maxTwoColPlanVariantRunes, maxSingleDigitButtonsPerRow)...)
	}

	rows = append(rows, []ports.Button{
		{Text: i18n.T(lang, "btn.approve_and_start"), Action: "plan_approve", Payload: strconv.Itoa(taskID)},
		{Text: i18n.T(lang, "btn.cancel"), Action: "plan_cancel", Payload: strconv.Itoa(taskID)},
	})
	rows = append(rows, []ports.Button{
		{Text: i18n.T(lang, "btn.download_plan"), Action: "plan_doc", Payload: strconv.Itoa(taskID)},
	})
	return &ports.Keyboard{Rows: rows}
}
