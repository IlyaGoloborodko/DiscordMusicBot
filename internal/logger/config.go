package logger

import (
	"errors"
	"os"
	"strconv"
)

type TelegramConfig struct {
	Token  string
	ChatID int64
}

func LoadTelegramConfig() (*TelegramConfig, error) {
	token := os.Getenv("TG_BOT_SECRET")
	chatIDStr := os.Getenv("TG_CHAT_ID")

	if token == "" {
		return nil, errors.New("TG_BOT_SECRET is empty")
	}
	if chatIDStr == "" {
		return nil, errors.New("TG_CHAT_ID is empty")
	}

	chatID, err := strconv.ParseInt(chatIDStr, 10, 64)
	if err != nil {
		return nil, err
	}

	return &TelegramConfig{
		Token:  token,
		ChatID: chatID,
	}, nil
}
