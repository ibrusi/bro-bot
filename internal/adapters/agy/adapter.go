package agy

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"bro-bot/internal/effort"
	"bro-bot/internal/models"
)

// agyBin — исполняемый файл CLI agy. Тесты подменяют его фальшивым агентом.
var agyBin = "agy"

// agyCLI выполняет вспомогательные запросы каталога моделей, квот и кредитов через CLI agy.
type agyCLI struct{}

func newAgyCLI() *agyCLI {
	return &agyCLI{}
}

// resolveAgyModelTarget переводит имя модели/алиас и уровень рассуждений в точный
// идентификатор варианта модели для agy и опциональный уровень effort.
func resolveAgyModelTarget(modelName string, level effort.Level) (modelID, effortVal string) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return "", ""
	}
	if resolved, ok := modelAliases.Resolve(modelName); ok {
		modelName = resolved
	}
	cmdArgs := models.BuildAgyModelArgs(modelName, level, models.KnownModelIDs())
	for i := 0; i < len(cmdArgs)-1; i++ {
		switch cmdArgs[i] {
		case "--model":
			modelID = cmdArgs[i+1]
		case "--effort":
			effortVal = cmdArgs[i+1]
		}
	}
	return modelID, effortVal
}

func (c *agyCLI) GetModels(ctx context.Context) ([]byte, error) {
	cmd := exec.CommandContext(ctx, agyBin, "models")
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, err
	}
	return out, nil
}

func (c *agyCLI) GetQuota(ctx context.Context, _ string) ([]byte, error) {
	return exec.CommandContext(ctx, agyBin, "-p", "/quota", "--output-format", "json").CombinedOutput()
}

func (c *agyCLI) GetQuotaText(ctx context.Context, _ string) ([]byte, error) {
	return exec.CommandContext(ctx, agyBin, "-p", "/quota").CombinedOutput()
}

func (c *agyCLI) GetCredits(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, agyBin, "-p", "/credits", "--output-format", "json").CombinedOutput()
}
