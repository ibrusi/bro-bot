package version

// Version хранит текущую версию bro-bot.
// Значение по умолчанию может переопределяться на этапе сборки через ldflags:
// -ldflags "-X bro-bot/internal/version.Version=..."
var Version = "1.0.0"
