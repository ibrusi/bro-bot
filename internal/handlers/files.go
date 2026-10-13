package handlers

import (
	"context"
	"fmt"
	"html"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"bro-bot/internal/config"
	"bro-bot/internal/domain"
	"bro-bot/internal/i18n"
	"bro-bot/internal/ports"
	"bro-bot/internal/utils"
)

const (
	maxTelegramFileSize = 20 * 1024 * 1024
	fileDownloadTimeout = 60 * time.Second
	uploadsDirName      = "uploads"
)

// handleDocument обрабатывает входящие файлы, документы и фотографии из Telegram.
func handleDocument(s ports.Session) error {
	file := s.File()
	if file == nil {
		return nil
	}

	lang := uiLang()

	// 1. Определение целевого проекта
	targetProj := ""
	var replyTask *domain.TaskSession
	if msg := s.Message(); msg != nil && msg.ReplyTo != nil {
		if t := domain.GlobalTaskManager.GetTaskByMessageID(msg.ReplyTo.ID); t != nil {
			replyTask = t
			targetProj = t.Snapshot().Project
		}
	}
	active := domain.GlobalTaskManager.GetActiveTask()
	if targetProj == "" && active != nil && (active.IsActive() || hasFreshPendingQuestion(active)) {
		targetProj = active.Snapshot().Project
	}
	if targetProj == "" {
		config.ProjectState.RLock()
		targetProj = config.ProjectState.CurrentProject
		config.ProjectState.RUnlock()
	}

	if targetProj == "" {
		return s.Send(i18n.T(lang, "projects.select_first"), ports.Rich())
	}

	// 2. Предварительная проверка размера файла
	if file.FileSize > maxTelegramFileSize {
		return s.Send(i18n.Tf(lang, "file.too_large", formatFileSize(file.FileSize)), ports.Rich())
	}

	safeName := utils.SanitizeUploadFileName(file.FileName)
	statusRef, err := s.Messenger().Send(context.Background(), s.Chat(),
		i18n.Tf(lang, "file.downloading", html.EscapeString(safeName)), ports.Rich())
	hasStatus := err == nil

	// 3. Подготовка целевой директории внутри проекта
	workDir := filepath.Join(config.ProjectsRoot, targetProj)
	targetDir := filepath.Join(workDir, uploadsDirName)
	if err := os.MkdirAll(targetDir, 0755); err != nil {
		log.Printf("file: mkdir %s failed: %v", targetDir, err)
		errMsg := i18n.Tf(lang, "file.save_failed", html.EscapeString(err.Error()))
		if hasStatus {
			_ = s.Messenger().Edit(context.Background(), statusRef, errMsg, ports.Rich())
		} else {
			_ = s.Send(errMsg, ports.Rich())
		}
		return err
	}

	// 4. Скачивание во временный файл
	tmpFile, err := os.CreateTemp(targetDir, ".upload-*.tmp")
	if err != nil {
		errMsg := i18n.Tf(lang, "file.save_failed", html.EscapeString(err.Error()))
		if hasStatus {
			_ = s.Messenger().Edit(context.Background(), statusRef, errMsg, ports.Rich())
		} else {
			_ = s.Send(errMsg, ports.Rich())
		}
		return err
	}
	tmpPath := tmpFile.Name()

	ctx, cancel := context.WithTimeout(context.Background(), fileDownloadTimeout)
	defer cancel()

	rc, err := s.OpenFile(ctx)
	if err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		errMsg := i18n.Tf(lang, "file.download_failed", html.EscapeString(err.Error()))
		if hasStatus {
			_ = s.Messenger().Edit(context.Background(), statusRef, errMsg, ports.Rich())
		} else {
			_ = s.Send(errMsg, ports.Rich())
		}
		return err
	}
	defer rc.Close()

	limitedReader := io.LimitReader(rc, maxTelegramFileSize+1)
	written, err := io.Copy(tmpFile, limitedReader)
	_ = tmpFile.Close()

	if err != nil {
		_ = os.Remove(tmpPath)
		errMsg := i18n.Tf(lang, "file.download_failed", html.EscapeString(err.Error()))
		if hasStatus {
			_ = s.Messenger().Edit(context.Background(), statusRef, errMsg, ports.Rich())
		} else {
			_ = s.Send(errMsg, ports.Rich())
		}
		return err
	}

	if written > maxTelegramFileSize {
		_ = os.Remove(tmpPath)
		errMsg := i18n.Tf(lang, "file.too_large", formatFileSize(written))
		if hasStatus {
			_ = s.Messenger().Edit(context.Background(), statusRef, errMsg, ports.Rich())
		} else {
			_ = s.Send(errMsg, ports.Rich())
		}
		return nil
	}

	// 5. Разрешение целевого пути с побайтовой дедупликацией
	destPath, isReused, err := utils.ResolveUploadDestination(targetDir, safeName, tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		errMsg := i18n.Tf(lang, "file.save_failed", html.EscapeString(err.Error()))
		if hasStatus {
			_ = s.Messenger().Edit(context.Background(), statusRef, errMsg, ports.Rich())
		} else {
			_ = s.Send(errMsg, ports.Rich())
		}
		return err
	}

	relPath, err := filepath.Rel(workDir, destPath)
	if err != nil {
		relPath = filepath.Join(uploadsDirName, filepath.Base(destPath))
	}

	// 6. Формирование промпта и статусного уведомления
	caption := strings.TrimSpace(file.Caption)
	attachmentNote := i18n.Tf(lang, "file.prompt_attachment", relPath, formatFileSize(written))
	var promptText string
	if caption != "" {
		promptText = fmt.Sprintf("%s\n\n%s", caption, attachmentNote)
	} else {
		promptText = attachmentNote
	}

	var statusNotice string
	baseDestName := filepath.Base(destPath)
	if isReused {
		statusNotice = i18n.Tf(lang, "file.reused_notice", html.EscapeString(baseDestName), relPath)
	} else {
		statusNotice = i18n.Tf(lang, "file.saved_notice", html.EscapeString(baseDestName), relPath)
	}
	if hasStatus {
		_ = s.Messenger().Edit(context.Background(), statusRef, statusNotice, ports.Rich())
	} else {
		_ = s.Send(statusNotice, ports.Rich())
	}

	// 7. Маршрутизация сообщения
	if replyTask != nil {
		return handleAddFollowupToTask(s, replyTask.Snapshot().ID, promptText)
	}
	if active != nil && (active.IsActive() || hasFreshPendingQuestion(active)) {
		return handleAddFollowupToTask(s, active.Snapshot().ID, promptText)
	}
	if config.ProjectState.GetInteractionMode() == domain.InteractionModeTask {
		config.ProjectState.RLock()
		planMode := config.ProjectState.PlanMode
		config.ProjectState.RUnlock()
		return handleCreateNewTaskWithOptions(s, promptText, planMode)
	}
	return handleTextInChatModeWithNote(s, promptText, "")
}

func formatFileSize(bytes int64) string {
	const (
		kb = 1024
		mb = 1024 * kb
	)
	if bytes >= mb {
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(mb))
	}
	if bytes >= kb {
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(kb))
	}
	return fmt.Sprintf("%d B", bytes)
}
