package logger

import (
	"context"
	"errors"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/redis/go-redis/v9"
)

type TelegramLogger struct {
	bot         *tgbotapi.BotAPI
	chatID      int64
	redisClient *redis.Client
}

func NewTelegramLogger(cfg *TelegramConfig, rdb *redis.Client) (*TelegramLogger, error) {
	if cfg == nil {
		return nil, errors.New("nil telegram config")
	}

	bot, err := tgbotapi.NewBotAPI(cfg.Token)
	if err != nil {
		return nil, err
	}

	return &TelegramLogger{
		bot:         bot,
		chatID:      cfg.ChatID,
		redisClient: rdb,
	}, nil
}

func (l *TelegramLogger) Send(ctx context.Context, text string) error {
	if l == nil || l.bot == nil {
		return errors.New("telegram logger is not initialized")
	}
	return l.redisClient.LPush(ctx, "tg_logs", text).Err()
}

var (
	tg   *TelegramLogger
	once sync.Once
)

func Init(ctx context.Context, l *TelegramLogger) {
	if l == nil {
		return
	}

	once.Do(func() {
		tg = l
		go tg.StartWorker(ctx)
	})
}

// Send reports an error. Kept because most call sites already build their text
// with fmt.Sprintf; Errorf is the shorter way to say the same thing.
//
// The returned error is about queueing, not about the log line, and every call
// site ignores it — Errorf drops it for that reason.
func Send(text string) error {
	logAt(LevelError, "%s", text)
	return nil
}

// send queues one already-composed line for Telegram. Unexported: what reaches
// Telegram is decided by level in logAt, in one place.
func send(text string) error {
	if tg == nil {
		return errors.New("telegram logger is not initialized")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	return tg.Send(ctx, text)
}
