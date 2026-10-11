package models

import (
	"bro-bot/internal/effort"
	"slices"
	"strings"
)

// Правила agy (с версии 1.3) для пары --model/--effort:
//   - вариант модели с уровнем в имени (claude-opus-5-5-high) запускается без
//     --effort, а другой уровень рядом с ним agy отвергает как конфликт;
//   - базовое имя семейства (claude-opus-5-5) без --effort не запускается, и
//     уровень допустим только из тех, для которых у семейства есть вариант.
//
// Поэтому для моделей из каталога уровень выражается точным ID варианта:
// такой ID agy гарантированно примет без дополнительных флагов.

// effortFamilies — уровни рассуждений по базовым именам моделей:
// "gemini-3.1-pro" → [low high]. Уровни отсортированы по возрастанию.
type effortFamilies map[string][]effort.Level

// familyMatch — семейство, к которому относится выбранная модель.
type familyMatch struct {
	base   string
	levels []effort.Level
	// ownLevel — уровень из имени выбранной модели; Auto, если выбрано базовое имя.
	ownLevel effort.Level
}

// BuildAgyModelArgs собирает флаги модели и усилия рассуждений для CLI agy.
//
// knownModels — ID моделей из каталога agy (`agy models`). Если выбранная модель
// относится к семейству вариантов по уровню, --model получает точный ID варианта:
//   - при effort.Auto выбранный вариант сохраняется, а базовому имени
//     подбирается уровень по умолчанию (defaultFamilyLevel);
//   - явный уровень (/effort) главнее уровня из имени: берётся вариант этого
//     уровня, а если его нет — ближайший доступный (closestLevel).
//
// Модели вне каталога обрабатываются по прежним эвристикам (legacyAgyModelArgs).
func BuildAgyModelArgs(modelName string, level effort.Level, knownModels []string) []string {
	family, ok := matchEffortFamily(modelName, groupEffortFamilies(knownModels))
	if !ok {
		return legacyAgyModelArgs(modelName, level)
	}
	return []string{"--model", family.variant(family.pickLevel(level))}
}

// KnownModelIDs возвращает ID моделей из каталога активного агента, а пока
// реестр не создан — из встроенного запасного списка.
func KnownModelIDs() []string {
	items := fallbackModels
	if GlobalModelRegistry != nil {
		items = GlobalModelRegistry.GetModels()
	}
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.ID)
	}
	return ids
}

// splitEffortSuffix отделяет уровень рассуждений от имени модели:
// "gemini-3.1-pro-high" → ("gemini-3.1-pro", High, true).
func splitEffortSuffix(modelID string) (string, effort.Level, bool) {
	for _, level := range effort.Levels() {
		if base, ok := strings.CutSuffix(modelID, "-"+level.String()); ok && base != "" {
			return base, level, true
		}
	}
	return modelID, effort.Auto, false
}

// groupEffortFamilies собирает семейства из каталога: модель попадает в
// семейство своего базового имени, если в её ID зашит уровень рассуждений.
func groupEffortFamilies(modelIDs []string) effortFamilies {
	families := make(effortFamilies)
	for _, id := range modelIDs {
		base, level, ok := splitEffortSuffix(id)
		if !ok || slices.Contains(families[base], level) {
			continue
		}
		families[base] = append(families[base], level)
	}
	for _, levels := range families {
		slices.SortFunc(levels, func(a, b effort.Level) int {
			return levelRank(a) - levelRank(b)
		})
	}
	return families
}

// matchEffortFamily находит семейство модели — по варианту из каталога
// (claude-opus-5-5-high) или по базовому имени (claude-opus-5-5).
func matchEffortFamily(modelName string, families effortFamilies) (familyMatch, bool) {
	if base, own, ok := splitEffortSuffix(modelName); ok {
		if levels, found := families[base]; found && slices.Contains(levels, own) {
			return familyMatch{base: base, levels: levels, ownLevel: own}, true
		}
	}
	if levels, found := families[modelName]; found {
		return familyMatch{base: modelName, levels: levels}, true
	}
	return familyMatch{}, false
}

// pickLevel выбирает уровень варианта: явный уровень главнее уровня из имени,
// а базовому имени без явного уровня достаётся уровень по умолчанию.
func (f familyMatch) pickLevel(requested effort.Level) effort.Level {
	switch {
	case !requested.IsAuto():
		return closestLevel(f.levels, requested)
	case !f.ownLevel.IsAuto():
		return f.ownLevel
	default:
		return defaultFamilyLevel(f.levels)
	}
}

// variant возвращает ID варианта семейства с уровнем level.
func (f familyMatch) variant(level effort.Level) string {
	return f.base + "-" + level.String()
}

// closestLevel подбирает уровень семейства для запрошенного: самый высокий из
// не превышающих запрошенный, а если таких нет — самый низкий из доступных.
// Так /effort max на модели с потолком high даёт high, а не ошибку agy.
// available не пуст и отсортирован по возрастанию.
func closestLevel(available []effort.Level, requested effort.Level) effort.Level {
	chosen := available[0]
	for _, level := range available {
		if levelRank(level) <= levelRank(requested) {
			chosen = level
		}
	}
	return chosen
}

// defaultFamilyLevel — уровень для базового имени при effort.Auto: без уровня
// agy такую модель не запустит. Сначала medium, затем high — прежние умолчания
// для Gemini Flash и Pro; иначе самый высокий из доступных.
func defaultFamilyLevel(available []effort.Level) effort.Level {
	for _, preferred := range []effort.Level{effort.Medium, effort.High} {
		if slices.Contains(available, preferred) {
			return preferred
		}
	}
	return available[len(available)-1]
}

// levelRank — порядковый номер явного уровня по возрастанию.
func levelRank(level effort.Level) int {
	return slices.Index(effort.Levels(), level)
}

// legacyAgyModelArgs — флаги для модели вне каталога (каталог ещё не загружен
// или модель задана вручную). Допустимость пары agy проверит сам.
func legacyAgyModelArgs(modelName string, level effort.Level) []string {
	args := []string{"--model", modelName}

	// Уровень уже зашит в имя: другой уровень рядом с ним agy отвергнет как конфликт.
	if _, _, hasLevel := splitEffortSuffix(modelName); hasLevel || strings.HasSuffix(modelName, "-thinking") {
		return args
	}

	// Прежние модели Claude (claude-sonnet-4-6) флаг --effort не принимают.
	if strings.Contains(modelName, "claude") {
		return args
	}

	if !level.IsAuto() {
		return append(args, "--effort", level.String())
	}

	// Для базовых Gemini моделей без суффикса effort
	if strings.Contains(modelName, "pro") {
		args = append(args, "--effort", effort.High.String())
	} else if strings.Contains(modelName, "gemini") {
		args = append(args, "--effort", effort.Medium.String())
	}

	return args
}
