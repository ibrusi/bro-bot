package handlers

import (
	"bro-bot/internal/config"
	"bro-bot/internal/ports"
	"context"
	"fmt"
	"html"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var validProjectNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_\.\-]+$`)

// parseRepoURL parses git SSH and HTTPS URLs, returning the clean URL and inferred repo name.
func parseRepoURL(rawURL string) (cleanURL, repoName string, err error) {
	u := strings.TrimSpace(rawURL)
	if u == "" {
		return "", "", fmt.Errorf("URL репозитория не может быть пустым")
	}

	if strings.HasPrefix(u, "-") {
		return "", "", fmt.Errorf("недопустимый формат URL")
	}

	trimmed := strings.TrimRight(u, "/")
	hasSCP := strings.Contains(trimmed, "@") && strings.Contains(trimmed, ":") && !strings.Contains(trimmed, "://")
	hasScheme := strings.HasPrefix(trimmed, "ssh://") ||
		strings.HasPrefix(trimmed, "https://") ||
		strings.HasPrefix(trimmed, "http://") ||
		strings.HasPrefix(trimmed, "git://")

	if !hasSCP && !hasScheme {
		return "", "", fmt.Errorf("неподдерживаемый протокол URL. Используйте SSH (git@...) или HTTPS (https://...)")
	}

	var name string
	if hasScheme {
		lastSlash := strings.LastIndex(trimmed, "/")
		if lastSlash == -1 || lastSlash == len(trimmed)-1 {
			return "", "", fmt.Errorf("не удалось определить имя репозитория из URL")
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
		return "", "", fmt.Errorf("не удалось определить корректное имя репозитория из URL")
	}

	return u, name, nil
}

// sanitizeProjectName validates and cleans the target folder name for a project.
func sanitizeProjectName(name string) (string, error) {
	clean := strings.TrimSpace(name)
	clean = strings.TrimSuffix(clean, ".git")

	if clean == "" {
		return "", fmt.Errorf("имя проекта не может быть пустым")
	}
	if strings.HasPrefix(clean, "-") {
		return "", fmt.Errorf("имя проекта не может начинаться с дефиса")
	}
	if clean == "." || clean == ".." {
		return "", fmt.Errorf("недопустимое имя проекта")
	}
	if strings.ContainsAny(clean, "/\\: \t\r\n") {
		return "", fmt.Errorf("имя проекта не должно содержать слэши, двоеточия или пробелы")
	}
	if !validProjectNameRegex.MatchString(clean) {
		return "", fmt.Errorf("имя проекта содержит недопустимые символы. Разрешены только буквы, цифры, дефис, подчеркивание и точка")
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
	args := s.Args()
	if len(args) == 0 {
		helpMsg := "📥 <b>Клонирование git-репозитория:</b>\n\n" +
			"Использование:\n" +
			"<code>/clone &lt;url&gt; [имя_папки]</code>\n\n" +
			"Примеры:\n" +
			"• <b>SSH:</b>\n" +
			"  <code>/clone git@github.com:owner/repo.git</code>\n" +
			"• <b>HTTPS:</b>\n" +
			"  <code>/clone https://github.com/owner/repo.git</code>\n" +
			"• <b>Своё имя папки:</b>\n" +
			"  <code>/clone git@github.com:owner/repo.git my-project</code>\n\n" +
			"💡 <i>Репозиторий будет сохранен в каталог проектов рядом с остальными проектами. Активный проект не переключается (для переключения используйте <code>/use &lt;имя&gt;</code>).</i>"
		return s.Send(helpMsg, ports.Rich())
	}

	rawURL := args[0]
	cleanURL, defaultName, err := parseRepoURL(rawURL)
	if err != nil {
		return s.Send(fmt.Sprintf("❌ Ошибка в URL: %s\n\nИспользование: <code>/clone &lt;url&gt; [имя_папки]</code>", html.EscapeString(err.Error())), ports.Rich())
	}

	targetName := defaultName
	if len(args) > 1 {
		customName, err := sanitizeProjectName(args[1])
		if err != nil {
			return s.Send(fmt.Sprintf("❌ Ошибка в имени проекта: %s", html.EscapeString(err.Error())), ports.Rich())
		}
		targetName = customName
	} else {
		validatedName, err := sanitizeProjectName(defaultName)
		if err != nil {
			return s.Send(fmt.Sprintf("❌ Не удалось использовать автоматически извлеченное имя <code>%s</code>: %s\nУкажите имя явно: <code>/clone %s &lt;имя&gt;</code>",
				html.EscapeString(defaultName), html.EscapeString(err.Error()), html.EscapeString(rawURL)), ports.Rich())
		}
		targetName = validatedName
	}

	cleanRoot := filepath.Clean(config.ProjectsRoot)
	if err := os.MkdirAll(cleanRoot, 0755); err != nil {
		return s.Send(fmt.Sprintf("❌ Ошибка доступа к каталогу проектов: %s", html.EscapeString(err.Error())), ports.Rich())
	}
	targetPath := filepath.Join(cleanRoot, targetName)
	cleanTargetPath := filepath.Clean(targetPath)

	if !strings.HasPrefix(cleanTargetPath, cleanRoot+string(filepath.Separator)) {
		return s.Send("❌ Недопустимый путь для проекта.", ports.Rich())
	}

	if _, err := os.Stat(targetPath); err == nil {
		return s.Send(fmt.Sprintf("❌ Каталог <code>%s</code> уже существует в проектах.\n\nДля переключения на него используйте: <code>/use %s</code>",
			html.EscapeString(targetName), html.EscapeString(targetName)), ports.Rich())
	}

	m := s.Messenger()
	chat := s.Chat()
	statusRef, err := m.Send(context.Background(), chat, fmt.Sprintf(
		"⏳ <b>Клонирование репозитория...</b>\n\n"+
			"🌐 <b>URL:</b> <code>%s</code>\n"+
			"📁 <b>Имя проекта:</b> <code>%s</code>\n\n"+
			"<i>Пожалуйста, подождите, выполняется git clone...</i>",
		html.EscapeString(cleanURL),
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

			outStr := strings.TrimSpace(string(out))
			if len(outStr) > 1500 {
				outStr = outStr[:1500] + "\n... (вывод обрезан)"
			}
			if outStr == "" {
				outStr = cloneErr.Error()
			}

			var hint strings.Builder
			hint.WriteString("\n\n💡 <b>Возможные причины ошибки:</b>")
			if strings.Contains(outStr, "Permission denied (publickey)") {
				hint.WriteString("\n• Ошибка доступа по SSH (publickey). Убедитесь, что публичный ключ сервера добавлен в репозиторий (Deploy Keys или аккаунт).")
				if pubKey := getSSHPublicKey(); pubKey != "" {
					hint.WriteString(fmt.Sprintf("\n\n🔑 <b>SSH-ключ сервера:</b>\n<code>%s</code>", html.EscapeString(pubKey)))
				}
			} else if strings.Contains(outStr, "Authentication failed") || strings.Contains(outStr, "could not read Username") {
				hint.WriteString("\n• Для приватных HTTPS-репозиториев укажите Personal Access Token в URL: <code>https://token@github.com/owner/repo.git</code> или используйте SSH.")
			} else if strings.Contains(outStr, "Could not resolve host") {
				hint.WriteString("\n• Не удалось найти хост. Проверьте правильность домена в URL.")
			} else if ctx.Err() == context.DeadlineExceeded {
				hint.WriteString("\n• Превышено время ожидания клонирования (5 минут). Проверьте доступность сети или размер репозитория.")
			} else {
				hint.WriteString("\n• Проверьте правильность URL репозитория и права доступа.")
			}

			errorMsg := fmt.Sprintf(
				"❌ <b>Не удалось склонировать репозиторий:</b>\n\n"+
					"<code>%s</code>%s",
				html.EscapeString(outStr),
				hint.String(),
			)

			if editErr := m.Edit(context.Background(), statusRef, errorMsg, ports.Rich()); editErr != nil {
				_ = s.Send(errorMsg, ports.Rich())
			}
			return
		}

		// Ensure AGENT.md exists in newly cloned repo
		agentDst := filepath.Join(targetPath, "AGENT.md")
		if _, err := os.Stat(agentDst); os.IsNotExist(err) {
			agentSrc := filepath.Join(cleanRoot, "AGENT.md")
			if _, srcErr := os.Stat(agentSrc); srcErr == nil {
				_ = copyFile(agentSrc, agentDst)
			}
		}

		// Do not switch current project; keep the existing active project
		config.ProjectState.RLock()
		curProj := config.ProjectState.CurrentProject
		config.ProjectState.RUnlock()

		var curProjInfo string
		if curProj != "" {
			curProjInfo = fmt.Sprintf("🎯 <b>Текущий активный проект:</b> <code>%s</code>\n\n", html.EscapeString(curProj))
		}

		successMsg := fmt.Sprintf(
			"✅ <b>Репозиторий успешно склонирован!</b>\n\n"+
				"📁 <b>Склонирован проект:</b> <code>%s</code>\n"+
				"🌐 <b>Источник:</b> <code>%s</code>\n"+
				"📂 <b>Путь:</b> <code>%s</code>\n"+
				"%s"+
				"💡 <i>Активный проект не изменился. Чтобы переключиться на склонированный проект, выполните:</i>\n"+
				"• <code>/use %s</code>",
			html.EscapeString(targetName),
			html.EscapeString(cleanURL),
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
