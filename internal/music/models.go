package music

// Track is a single search result returned by the media-source-service.
type Track struct {
	Title    string `json:"title"`
	ID       string `json:"id"`
	Uploader string `json:"uploader"`
	URL      string `json:"url"`
}

// searchResponse mirrors GET /search of the media-source-service.
type searchResponse struct {
	Provider string  `json:"provider"`
	Query    string  `json:"query"`
	Results  []Track `json:"results"`
}

// streamResponse mirrors GET /stream of the media-source-service.
type streamResponse struct {
	Provider  string `json:"provider"`
	ID        string `json:"id"`
	StreamURL string `json:"stream_url"`
}
