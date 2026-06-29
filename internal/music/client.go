package music

import (
	"net/http"
	"os"
	"strings"
	"time"
)

const (
	defaultSearchServiceAddr = "http://127.0.0.1:9000"
	providerYouTube          = "youtube"
	searchLimit              = "10"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

// searchServiceAddr returns the base URL of the media-source-service,
// overridable via SEARCH_SERVICE_ADDR.
func searchServiceAddr() string {
	addr := strings.TrimSpace(os.Getenv("SEARCH_SERVICE_ADDR"))
	if addr == "" {
		addr = defaultSearchServiceAddr
	}
	return strings.TrimRight(addr, "/")
}
