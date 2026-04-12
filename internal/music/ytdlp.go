package music

import (
	"log"
	"os"
	"os/exec"
	"strings"
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

func LogYTDLPVersion() {
	out, err := exec.Command(ytDLPBinary(), "--version").Output()
	if err != nil {
		log.Printf("yt-dlp version check failed: %v", err)
		return
	}
	log.Printf("yt-dlp binary: %s, version: %s", ytDLPBinary(), strings.TrimSpace(string(out)))
}
