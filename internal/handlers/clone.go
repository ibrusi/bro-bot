package handlers

import (
	"bro-bot/internal/config"
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
	"bro-bot/internal/utils"
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Ошибки разбора ссылки на репозиторий. Текст служебный: перевод для пользователя
// подбирает ErrorText.
var (
	errRepoURLEmpty       = errors.New("clone: repository URL must not be empty")
	errRepoURLInvalid     = errors.New("clone: invalid URL format")
	errRepoURLProtocol    = errors.New("clone: unsupported URL protocol")
	errRepoNameUndetected = errors.New("clone: cannot determine the repository name from the URL")
	errRepoNameInvalid    = errors.New("clone: the repository name from the URL is not usable")
)

// parseRepoURL parses git SSH and HTTPS URLs, returning the clean URL and inferred repo name.
func parseRepoURL(rawURL string) (cleanURL, repoName string, err error) {
	u := strings.TrimSpace(rawURL)
	if u == "" {
		return "", "", errRepoURLEmpty
	}

	if strings.HasPrefix(u, "-") {
		return "", "", errRepoURLInvalid
	}

	trimmed := strings.TrimRight(u, "/")
	hasSCP := strings.Contains(trimmed, "@") && strings.Contains(trimmed, ":") && !strings.Contains(trimmed, "://")
	hasScheme := strings.HasPrefix(trimmed, "ssh://") ||
		strings.HasPrefix(trimmed, "https://") ||
		strings.HasPrefix(trimmed, "http://") ||
		strings.HasPrefix(trimmed, "git://")

	if !hasSCP && !hasScheme {
		return "", "", errRepoURLProtocol
	}

	var name string
	if hasScheme {
		lastSlash := strings.LastIndex(trimmed, "/")
		if lastSlash == -1 || lastSlash == len(trimmed)-1 {
			return "", "", errRepoNameUndetected
		}
		name = trimmed[lastSlash+1:]
	} else {
		parts := strings.Split(trimmed, ":")
		pathPart := parts[len(parts)-1]
		lastSlash := strings.LastIndex(pathPart, "/")
		if lastSlash != -1 {
			name = pathPart[lastSlash+1:]
		} else {
			name = pathPart
		}
	}

	name = strings.TrimSuffix(name, ".git")
	name = strings.TrimSpace(name)

	if name == "" || name == "." || name == ".." {
		return "", "", errRepoNameInvalid
	}

	return u, name, nil
}

// sanitizeProjectName validates and cleans the target folder name for a project.
// Общие правила для сегмента пути живут в utils.SanitizeSegment — здесь остаётся
// только специфичное для репозиториев отбрасывание суффикса .git.
func sanitizeProjectName(name string) (string, error) {
	clean := strings.TrimSuffix(strings.TrimSpace(name), ".git")

	clean, err := utils.SanitizeSegment(clean)
	if err != nil {
		return "", fmt.Errorf("clone: project name: %w", err)
	}
	return clean, nil
}

// copyFile copies a single file from src to dst.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// getSSHPublicKey attempts to find and return the deploy SSH public key for helper messages.
func getSSHPublicKey() string {
	home := os.Getenv("HOME")
	if home == "" {
		home = "/home/deploy"
	}
	candidates := []string{
		filepath.Join(home, ".ssh", "id_ed25519.pub"),
		filepath.Join(home, ".ssh", "id_rsa.pub"),
	}
	for _, p := range candidates {
		if data, err := os.ReadFile(p); err == nil {
			trimmed := strings.TrimSpace(string(data))
			if trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

// cloneRepository executes git clone in a child process with non-interactive flags.
func cloneRepository(ctx context.Context, repoURL, targetPath string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", "clone", "--", repoURL, targetPath)
	cmd.Env = append(os.Environ(),
		"GIT_TERMINAL_PROMPT=0",
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes -o StrictHostKeyChecking=accept-new",
	)
	return cmd.CombinedOutput()
}

// handleCloneCommand обрабатывает команду /clone <url> [имя].
func handleCloneCommand(s ports.Session) error {
	lang := uiLang()

	args := s.Args()
	if len(args) == 0 {
		return s.Send(i18n.T(lang, "clone.help"), ports.Rich())
	}

	rawURL := args[0]
	cleanURL, defaultName, err := parseRepoURL(rawURL)
	if err != nil {
		return s.Send(i18n.Tf(lang, "clone.url_error", html.EscapeString(ErrorText(err, lang))), ports.Rich())
	}

	// В чат и в лог уходит ссылка без учётных данных: в HTTPS-URL часто встраивают
	// токен доступа, и он не должен оставаться в истории переписки. Сам git clone
	// ниже получает полный cleanURL.
	displayURL := utils.RedactURLCredentials(cleanURL)

	targetName := defaultName
	if len(args) > 1 {
		customName, err := sanitizeProjectName(args[1])
		if err != nil {
			return s.Send(i18n.Tf(lang, "clone.name_error", html.EscapeString(ErrorText(err, lang))), ports.Rich())
		}
		targetName = customName
	} else {
		validatedName, err := sanitizeProjectName(defaultName)
		if err != nil {
			return s.Send(i18n.Tf(lang, "clone.autoname_error",
				html.EscapeString(defaultName), html.EscapeString(ErrorText(err, lang)), html.EscapeString(displayURL)), ports.Rich())
		}
		targetName = validatedName
	}

	cleanRoot := filepath.Clean(config.ProjectsRoot)
	if err := os.MkdirAll(cleanRoot, 0755); err != nil {
		return s.Send(i18n.Tf(lang, "clone.root_error", html.EscapeString(err.Error())), ports.Rich())
	}

	targetPath, err := utils.SafeJoinSegment(cleanRoot, targetName)
	if err != nil {
		return s.Send(i18n.Tf(lang, "clone.path_error", html.EscapeString(ErrorText(err, lang))), ports.Rich())
	}

	if _, err := os.Stat(targetPath); err == nil {
		return s.Send(i18n.Tf(lang, "clone.exists",
			html.EscapeString(targetName), html.EscapeString(targetName)), ports.Rich())
	}

	m := s.Messenger()
	chat := s.Chat()
	statusRef, err := m.Send(context.Background(), chat, i18n.Tf(lang, "clone.progress",
		html.EscapeString(displayURL),
		html.EscapeString(targetName),
	), ports.Rich())
	if err != nil {
		return err
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()

		out, cloneErr := cloneRepository(ctx, cleanURL, targetPath)
		if cloneErr != nil {
			_ = os.RemoveAll(targetPath)

			// git цитирует адрес репозитория в своих ошибках ("Authentication failed
			// for 'https://token@...'"), поэтому вывод тоже чистим от учётных данных.
			outStr := strings.TrimSpace(utils.RedactURLCredentials(string(out)))
			outStr = utils.TruncateWithNote(outStr, 1500, i18n.T(lang, "clone.output_truncated"))
			if outStr == "" {
				outStr = cloneErr.Error()
			}

			var hint strings.Builder
			hint.WriteString(i18n.T(lang, "clone.hint_header"))
			if strings.Contains(outStr, "Permission denied (publickey)") {
				hint.WriteString(i18n.T(lang, "clone.hint_publickey"))
				if pubKey := getSSHPublicKey(); pubKey != "" {
					hint.WriteString(i18n.Tf(lang, "clone.hint_sshkey", html.EscapeString(pubKey)))
				}
			} else if strings.Contains(outStr, "Authentication failed") || strings.Contains(outStr, "could not read Username") {
				hint.WriteString(i18n.T(lang, "clone.hint_auth"))
			} else if strings.Contains(outStr, "Could not resolve host") {
				hint.WriteString(i18n.T(lang, "clone.hint_host"))
			} else if ctx.Err() == context.DeadlineExceeded {
				hint.WriteString(i18n.T(lang, "clone.hint_timeout"))
			} else {
				hint.WriteString(i18n.T(lang, "clone.hint_generic"))
			}

			errorMsg := i18n.Tf(lang, "clone.failed",
				html.EscapeString(outStr),
				hint.String(),
			)

			if editErr := m.Edit(context.Background(), statusRef, errorMsg, ports.Rich()); editErr != nil {
				_ = s.Send(errorMsg, ports.Rich())
			}
			return
		}

		// Ensure AGENTS.md exists in newly cloned repo
		agentDst := filepath.Join(targetPath, "AGENTS.md")
		if _, err := os.Stat(agentDst); os.IsNotExist(err) {
			agentSrc := filepath.Join(cleanRoot, "AGENTS.md")
			if _, srcErr := os.Stat(agentSrc); srcErr == nil {
				_ = copyFile(agentSrc, agentDst)
			}
		}

		// Ensure CLAUDE.md symlink exists for native Claude Code CLI discovery
		claudeDst := filepath.Join(targetPath, "CLAUDE.md")
		if _, err := os.Stat(claudeDst); os.IsNotExist(err) {
			_ = os.Symlink("AGENTS.md", claudeDst)
		}

		// Do not switch current project; keep the existing active project
		config.ProjectState.RLock()
		curProj := config.ProjectState.CurrentProject
		config.ProjectState.RUnlock()

		var curProjInfo string
		if curProj != "" {
			curProjInfo = i18n.Tf(lang, "clone.current_project", html.EscapeString(curProj))
		}

		successMsg := i18n.Tf(lang, "clone.success",
			html.EscapeString(targetName),
			html.EscapeString(displayURL),
			html.EscapeString(targetPath),
			curProjInfo,
			html.EscapeString(targetName),
		)

		if editErr := m.Edit(context.Background(), statusRef, successMsg, ports.Rich()); editErr != nil {
			_ = s.Send(successMsg, ports.Rich())
		}
	}()

	return nil
}
