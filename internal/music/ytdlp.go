package music

import (
	"os"
	"sync"
)

const dockerYTDLPPath = "/opt/yt/bin/yt-dlp"

var (
	ytDLPBin     string
	ytDLPBinOnce sync.Once
)

func ytDLPBinary() string {
	ytDLPBinOnce.Do(func() {
		if info, err := os.Stat(dockerYTDLPPath); err == nil && !info.IsDir() {
			ytDLPBin = dockerYTDLPPath
			return
		}
		ytDLPBin = "yt-dlp"
	})
	return ytDLPBin
}
