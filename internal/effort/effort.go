// Package effort описывает уровень усилия рассуждений агента (команда /effort).
//
// Шкала общая для agy и claude: оба CLI принимают флаг --effort с одними и теми
// же значениями, а Claude API — поле output_config.effort. Пакет не зависит от
// остального кода, поэтому значение можно передавать через порты в адаптеры.
package effort

import "strings"

// Level — уровень усилия рассуждений. Нулевое значение означает Auto: бот не
// передаёт уровень, и агент выбирает его сам.
type Level string

const (
	// Auto — уровень не задан: агент и модель решают сами.
	Auto Level = ""
	// Low — быстрые ответы с минимумом рассуждений.
	Low Level = "low"
	// Medium — баланс скорости и глубины.
	Medium Level = "medium"
	// High — глубокие рассуждения.
	High Level = "high"
	// XHigh — уровень между High и Max.
	XHigh Level = "xhigh"
	// Max — максимальная глубина без оглядки на расход токенов.
	Max Level = "max"
)

// autoName — как Auto пишется в командах и хранится в базе.
const autoName = "auto"

// explicitLevels — явные уровни в порядке возрастания.
var explicitLevels = []Level{Low, Medium, High, XHigh, Max}

// Levels возвращает явные уровни в порядке возрастания (без Auto).
func Levels() []Level {
	out := make([]Level, len(explicitLevels))
	copy(out, explicitLevels)
	return out
}

// Parse разбирает уровень из ввода пользователя или из базы без учёта регистра.
// Второе значение — false, если строка не является уровнем.
func Parse(raw string) (Level, bool) {
	value := strings.ToLower(strings.TrimSpace(raw))
	if value == autoName {
		return Auto, true
	}
	for _, level := range explicitLevels {
		if string(level) == value {
			return level, true
		}
	}
	return Auto, false
}

// IsAuto сообщает, что уровень не задан и агенту его передавать не нужно.
func (l Level) IsAuto() bool {
	return l == Auto
}

// String возвращает имя уровня для команд, сообщений и хранения в базе.
func (l Level) String() string {
	if l.IsAuto() {
		return autoName
	}
	return string(l)
}
