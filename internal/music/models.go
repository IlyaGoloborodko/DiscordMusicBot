package music

type Track struct {
	Title    string `json:"title"`
	ID       string `json:"id"`
	Uploader string `json:"uploader"`
}

type searchVariant struct {
	name string
	args []string
}
