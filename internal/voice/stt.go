package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"unicode"
)

// Discord delivers 48kHz stereo; whisper.cpp wants 16kHz mono.
const (
	recvSampleRate = 48000
	recvChannels   = 2
	sttSampleRate  = 16000
)

// wakeWords are normalized (lowercase, ё→е) trigger phrases. The bot acts on a
// transcribed utterance only if it contains one of these. Extend freely.
var wakeWords = []string{
	"бебей", "бэбей", "бебэй", "бэбэй",
	"бейби", "бэйби", "бейбэ", "бэйбэ",
	"бебис", "бэбис", "бибис", "бэбис",
	"беби", "бэби", "бейб", "бэйб",
	"baby", "bebis", "beibi",
}

func whisperBin() string {
	if v := strings.TrimSpace(os.Getenv("WHISPER_BIN")); v != "" {
		return v
	}
	return "whisper-cli"
}

func whisperModel() string {
	return strings.TrimSpace(os.Getenv("WHISPER_MODEL"))
}

// normalize lowercases, folds ё→е and replaces non-letters/digits with spaces so
// wake-word matching is robust to punctuation/casing in the transcript.
func normalize(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "ё", "е")
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune(' ')
		}
	}
	return b.String()
}

func containsWakeWord(text string) bool {
	n := normalize(text)
	for _, w := range wakeWords {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// transcribe runs whisper.cpp (exec) on 16kHz-mono PCM, returning the raw text.
func transcribe(ctx context.Context, mono []int16) (string, error) {
	model := whisperModel()
	if model == "" {
		return "", fmt.Errorf("WHISPER_MODEL is not set")
	}

	wav := pcmToWAV(mono, sttSampleRate, 1)

	tmp, err := os.CreateTemp("", "utt-*.wav")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(wav); err != nil {
		_ = tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}

	cmd := exec.CommandContext(ctx, whisperBin(),
		"-m", model,
		"-l", "ru",
		"-nt", // no timestamps -> plain text on stdout
		"-f", tmpName,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("whisper failed: %w; stderr: %s", err, strings.TrimSpace(stderr.String()))
	}

	return cleanWhisperOutput(stdout.String()), nil
}

func cleanWhisperOutput(s string) string {
	var parts []string
	for _, ln := range strings.Split(s, "\n") {
		if ln = strings.TrimSpace(ln); ln != "" {
			parts = append(parts, ln)
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// rms returns the root-mean-square amplitude of the PCM (loudness proxy).
func rms(pcm []int16) float64 {
	if len(pcm) == 0 {
		return 0
	}
	var sum float64
	for _, s := range pcm {
		sum += float64(s) * float64(s)
	}
	return math.Sqrt(sum / float64(len(pcm)))
}

// trimSilence keeps only the region around audible content (drops leading/trailing
// silence — e.g. the up-to-3s pause we wait for — so whisper doesn't hallucinate
// on silence). Returns nil if nothing is above the amplitude threshold.
func trimSilence(mono []int16) []int16 {
	const amp = 350     // int16 amplitude considered "speech"
	const margin = 1600 // ~0.1s @16k kept around speech

	first, last := -1, -1
	for i, s := range mono {
		if s < 0 {
			s = -s
		}
		if int(s) > amp {
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if first < 0 {
		return nil
	}
	if first -= margin; first < 0 {
		first = 0
	}
	if last += margin; last > len(mono) {
		last = len(mono)
	}
	return mono[first:last]
}

var (
	reBracket = regexp.MustCompile(`\[[^\]]*\]`)
	reParen   = regexp.MustCompile(`\([^)]*\)`)
	reStar    = regexp.MustCompile(`\*[^*]*\*`)
	reSpaces  = regexp.MustCompile(`\s+`)

	// Substrings that mark a whisper hallucination (YouTube-subtitle artifacts).
	hallucinationMarkers = []string{
		"редактор субтитров", "корректор", "субтитры",
		"подпишись", "продолжение следует", "спасибо за просмотр",
		"ставьте лайк", "amara", "dimatorzok",
	}
)

// cleanTranscript strips sound-effect tags ([музыка], (аплодисменты), *...*) and
// drops known hallucinated credit lines. Returns "" if nothing real remains.
func cleanTranscript(s string) string {
	s = reBracket.ReplaceAllString(s, " ")
	s = reParen.ReplaceAllString(s, " ")
	s = reStar.ReplaceAllString(s, " ")
	s = strings.TrimSpace(reSpaces.ReplaceAllString(s, " "))
	if s == "" {
		return ""
	}
	n := normalize(s)
	for _, m := range hallucinationMarkers {
		if strings.Contains(n, m) {
			return ""
		}
	}
	return s
}

// downmixTo16kMono converts interleaved 48kHz stereo PCM to 16kHz mono using the
// LEFT channel only. Discord's two channels can be decorrelated (observed L-R
// correlation ~0.5); averaging L+R comb-filters the speech into mush. 48000/16000
// is exactly 3, so we decimate the left channel by 3 through a triangular 5-tap
// low-pass (weights 1,2,3,2,1) to keep aliasing out of the 16k band.
func downmixTo16kMono(pcm []int16) []int16 {
	n := len(pcm) / 2 // left-channel sample count
	if n == 0 {
		return nil
	}
	left := func(i int) int {
		if i < 0 {
			i = 0
		} else if i >= n {
			i = n - 1
		}
		return int(pcm[i*2])
	}
	out := make([]int16, 0, n/3+1)
	for c := 0; c < n; c += 3 {
		v := left(c-2) + 2*left(c-1) + 3*left(c) + 2*left(c+1) + left(c+2)
		out = append(out, int16(v/9))
	}
	return out
}

// pcmToWAV wraps interleaved 16-bit PCM in a minimal RIFF/WAVE container.
func pcmToWAV(pcm []int16, sampleRate, channels int) []byte {
	dataSize := len(pcm) * 2

	var buf bytes.Buffer
	buf.WriteString("RIFF")
	binary.Write(&buf, binary.LittleEndian, uint32(36+dataSize))
	buf.WriteString("WAVE")
	buf.WriteString("fmt ")
	binary.Write(&buf, binary.LittleEndian, uint32(16))
	binary.Write(&buf, binary.LittleEndian, uint16(1)) // PCM
	binary.Write(&buf, binary.LittleEndian, uint16(channels))
	binary.Write(&buf, binary.LittleEndian, uint32(sampleRate))
	binary.Write(&buf, binary.LittleEndian, uint32(sampleRate*channels*2)) // byte rate
	binary.Write(&buf, binary.LittleEndian, uint16(channels*2))            // block align
	binary.Write(&buf, binary.LittleEndian, uint16(16))                    // bits per sample
	buf.WriteString("data")
	binary.Write(&buf, binary.LittleEndian, uint32(dataSize))
	binary.Write(&buf, binary.LittleEndian, pcm)
	return buf.Bytes()
}
