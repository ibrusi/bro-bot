package models

import "strings"

// AliasHint — псевдоним, который /model показывает в подсказке, и название модели,
// на которую он указывает.
type AliasHint struct {
	Alias string
	Title string
}

// Aliases — псевдонимы моделей одного агента: короткое имя → ID модели в его каталоге.
//
// У каждого агента набор свой: одно и то же имя может вести к разным моделям
// (или отсутствовать), поэтому псевдоним разрешается только в контексте агента.
// Значение неизменяемо после создания и безопасно для конкурентного чтения.
type Aliases struct {
	byName map[string]string
	hints  []AliasHint
}

// NewAliases создаёт набор псевдонимов. Имена приводятся к нижнему регистру, как и ввод
// пользователя в Resolve; hints — подсказки для /model в порядке показа.
func NewAliases(byName map[string]string, hints ...AliasHint) Aliases {
	normalized := make(map[string]string, len(byName))
	for name, model := range byName {
		normalized[normalizeAlias(name)] = model
	}
	return Aliases{
		byName: normalized,
		hints:  append([]AliasHint(nil), hints...),
	}
}

// Resolve возвращает ID модели для псевдонима.
func (a Aliases) Resolve(name string) (string, bool) {
	model, ok := a.byName[normalizeAlias(name)]
	return model, ok
}

// Hints возвращает подсказки для /model.
func (a Aliases) Hints() []AliasHint {
	return append([]AliasHint(nil), a.hints...)
}

func normalizeAlias(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}
