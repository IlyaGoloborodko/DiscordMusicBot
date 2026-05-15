package logger

import (
	"context"
	"fmt"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func (l *TelegramLogger) StartWorker(ctx context.Context) {
	for {
		res, err := l.redisClient.BRPop(ctx, 0, "tg_logs").Result()
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			fmt.Println("redis error:", err)
			continue
		}

		text := res[1]

		msg := tgbotapi.NewMessage(l.chatID, text)
		_, err = l.bot.Send(msg)
		if err != nil {
			fmt.Println("failed to send telegram:", err)
		}
	}
}
