package agents

import (
	"bro-bot/internal/i18n"
	"errors"
	"strings"
	"testing"

	"bro-bot/internal/ports"
)

type fakeFramework struct {
	ports.AgentFramework
	kind string
}

func testSpec(name string) Spec {
	return Spec{
		Name:      name,
		CLITitle:  name + " CLI",
		APITitle:  name + " API",
		MCPTitle:  name + " MCP",
		APIKeyEnv: []string{strings.ToUpper(name) + "_KEY"},
		NewCLI:    func() ports.AgentFramework { return &fakeFramework{kind: "cli"} },
		NewAPI:    func() ports.AgentFramework { return &fakeFramework{kind: "api"} },
		NewMCP:    func() ports.AgentFramework { return &fakeFramework{kind: "mcp"} },
	}
}

func TestRegistryOrderAndDefault(t *testing.T) {
	reg := NewRegistry()
	if reg.Default() != "" || len(reg.Names()) != 0 {
		t.Fatal("пустой реестр должен быть пустым")
	}

	reg.Register(testSpec("Alpha"))
	reg.Register(testSpec("beta"))
	reg.Register(testSpec("ALPHA")) // повторная регистрация не меняет порядок

	if got := reg.Names(); strings.Join(got, ",") != "alpha,beta" {
		t.Errorf("Names = %v, ожидали alpha,beta", got)
	}
	if reg.Default() != "alpha" {
		t.Errorf("Default = %q, ожидали первого зарегистрированного", reg.Default())
	}
}

func TestRegistryLookupIsCaseInsensitive(t *testing.T) {
	reg := NewRegistry()
	reg.Register(testSpec("alpha"))

	for _, name := range []string{"alpha", "ALPHA", "  Alpha "} {
		if !reg.Known(name) {
			t.Errorf("Known(%q) = false", name)
		}
		if reg.Normalize(name) != "alpha" {
			t.Errorf("Normalize(%q) = %q", name, reg.Normalize(name))
		}
	}
	if reg.Known("gamma") {
		t.Error("незарегистрированный агент не должен быть известен")
	}
	if reg.Normalize("gamma") != "alpha" || reg.Normalize("") != "alpha" {
		t.Error("неизвестное и пустое имя должны превращаться в агента по умолчанию")
	}
}

func TestRegistryTitle(t *testing.T) {
	reg := NewRegistry()
	reg.Register(testSpec("alpha"))

	cases := map[[2]string]string{
		{"alpha", "cli"}:   "alpha CLI",
		{"alpha", "api"}:   "alpha API",
		{"alpha", "API"}:   "alpha API",
		{"alpha", "mcp"}:   "alpha MCP",
		{"alpha", "MCP"}:   "alpha MCP",
		{"alpha", ""}:      "alpha MCP",
		{"unknown", "api"}: "agent API",
		{"unknown", "mcp"}: "MCP channel",
		{"", "cli"}:        "agent CLI",
		{"", ""}:           "MCP channel",
	}
	for in, want := range cases {
		if got := reg.Title(in[0], in[1], i18n.Default); got != want {
			t.Errorf("Title(%q, %q) = %q, ожидали %q", in[0], in[1], got, want)
		}
	}
}

func TestRegistryBuildPicksModeAndChecksKey(t *testing.T) {
	reg := NewRegistry()
	reg.Register(testSpec("alpha"))

	fw, err := reg.Build("alpha", "cli")
	if err != nil {
		t.Fatalf("cli: %v", err)
	}
	if fw.(*fakeFramework).kind != "cli" {
		t.Errorf("cli: собран %s", fw.(*fakeFramework).kind)
	}

	t.Setenv("ALPHA_KEY", "")
	if _, err := reg.Build("alpha", "api"); err == nil {
		t.Fatal("без ключа api-режим должен отказывать")
	} else {
		var missing *MissingAPIKeyError
		if !errors.As(err, &missing) {
			t.Fatalf("ожидали MissingAPIKeyError, получили %T: %v", err, err)
		}
		if strings.ContainsAny(err.Error(), "<>") {
			t.Errorf("текст ошибки не должен содержать разметку: %s", err)
		}
		if !strings.Contains(err.Error(), "ALPHA_KEY") {
			t.Errorf("ошибка должна называть переменную: %s", err)
		}
	}

	t.Setenv("ALPHA_KEY", "secret")
	fw, err = reg.Build("alpha", "api")
	if err != nil {
		t.Fatalf("api с ключом: %v", err)
	}
	if fw.(*fakeFramework).kind != "api" {
		t.Errorf("api: собран %s", fw.(*fakeFramework).kind)
	}

	fw, err = reg.Build("alpha", "mcp")
	if err != nil {
		t.Fatalf("mcp: %v", err)
	}
	if fw.(*fakeFramework).kind != "mcp" {
		t.Errorf("mcp: собран %s", fw.(*fakeFramework).kind)
	}
}

func TestRegistryBuildUnknownAgent(t *testing.T) {
	reg := NewRegistry()
	reg.Register(testSpec("alpha"))
	reg.Register(testSpec("beta"))

	_, err := reg.Build("gamma", "cli")
	var unknown *UnknownAgentError
	if !errors.As(err, &unknown) {
		t.Fatalf("ожидали UnknownAgentError, получили %T: %v", err, err)
	}
	if !strings.Contains(err.Error(), "alpha, beta") {
		t.Errorf("ошибка должна перечислять известных агентов: %s", err)
	}
	if strings.ContainsAny(err.Error(), "<>") {
		t.Errorf("текст ошибки не должен содержать разметку: %s", err)
	}
}

func TestNormalizeMode(t *testing.T) {
	for in, want := range map[string]string{
		"api":    "api",
		" API ":  "api",
		"cli":    "cli",
		"mcp":    "mcp",
		" MCP ":  "mcp",
		"":       "mcp",
		"что-то": "mcp",
	} {
		if got := NormalizeMode(in); got != want {
			t.Errorf("NormalizeMode(%q) = %q, ожидали %q", in, got, want)
		}
	}
}
