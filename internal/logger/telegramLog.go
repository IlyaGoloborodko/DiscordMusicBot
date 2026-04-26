package logger

import (
	"errors"
	"log"
	"sync"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type TelegramLogger struct {
	bot    *tgbotapi.BotAPI
	chatID int64
}

func NewTelegramLogger(cfg *TelegramConfig) (*TelegramLogger, error) {
	if cfg == nil {
		return nil, errors.New("nil telegram config")
	}

	bot, err := tgbotapi.NewBotAPI(cfg.Token)
	if err != nil {
		return nil, err
	}

	return &TelegramLogger{
		bot:    bot,
		chatID: cfg.ChatID,
	}, nil
}

func (l *TelegramLogger) Send(text string) error {
	if l == nil || l.bot == nil {
		return errors.New("telegram logger is not initialized")
	}

	msg := tgbotapi.NewMessage(l.chatID, text)
	_, err := l.bot.Send(msg)
	return err
}

var (
	tg   *TelegramLogger
	once sync.Once
)

func Init(l *TelegramLogger) {
	once.Do(func() {
		tg = l
	})
}

func Send(text string) error {
	if tg == nil {
		log.Printf("failed to send telegram message")
	}
	return tg.Send(text)
}
