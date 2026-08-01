// Command adminui is the admin panel's gateway: it authenticates people with
// Discord, serves the panel, and proxies its requests to the bot and the AI
// service with a service token they trust.
//
// It is a separate binary from the bot on purpose. The panel stays reachable
// while the bot restarts — which is exactly when somebody wants to look at it —
// and neither service ends up owning the other's settings.
package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"

	"discordAudio/internal/adminui"

	"github.com/joho/godotenv"
)

func main() {
	// Overload, matching the bot: the file wins over the ambient environment, so
	// a stale machine-wide variable cannot quietly beat the .env just edited.
	if err := godotenv.Overload(); err != nil {
		log.Println(".env file not found, using system environment variables")
	}

	gw, err := adminui.New(adminui.ConfigFromEnv())
	if err != nil {
		// Fatal here, unlike in the bot. The bot must keep playing music through
		// a broken diagnostic; this process IS the diagnostic, and one that
		// starts misconfigured would be an open door rather than a missing one.
		log.Fatalf("admin gateway: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := gw.ListenAndServe(ctx); err != nil {
		log.Printf("admin gateway stopped: %v", err)
		os.Exit(1)
	}
}
