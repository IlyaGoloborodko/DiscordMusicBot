package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"regexp"
	"strings"
	"unicode"

	"github.com/gorilla/websocket"
)

// Discord delivers 48kHz stereo; whisper.cpp wants 16kHz mono.
const (
	recvSampleRate = 48000
	recvChannels   = 2
	sttSampleRate  = 16000
)

// wakeWords are normalized (lowercase, ё→е) trigger phrases. The bot treats an
// utterance as an AI command only if it contains one of these ("Марина" and how
// whisper/vosk tend to hear it). Matching is substring-based, so "марин" also
// catches марину/марине/маринка etc. Extend freely.
var wakeWords = []string{
	"марин", "марина", "маринка", "мариночка",
	"мариш", "мариша", "маришка",
	"marina", "marin",
}

// STT log verbosity levels, selected via the STT_LOG_LEVEL env var.
const (
	sttLogSilent   = 0 // nothing
	sttLogCommands = 1 // only wake-word commands (text after "Марина") + AI round-trip
	sttLogAll      = 2 // every transcribed utterance (full whisper output)
)

// sttLogLevel reads STT_LOG_LEVEL (0/1/2); defaults to sttLogCommands.
func sttLogLevel() int {
	switch strings.TrimSpace(os.Getenv("STT_LOG_LEVEL")) {
	case "0":
		return sttLogSilent
	case "2":
		return sttLogAll
	default:
		return sttLogCommands
	}
}

// whisperServerAddr is the base URL of the whisper HTTP service (e.g.
// onerahmet/openai-whisper-asr-webservice), like "http://127.0.0.1:9010".
func whisperServerAddr() string {
	return strings.TrimSpace(os.Getenv("WHISPER_SERVER_ADDR"))
}

// voskServerAddr is the websocket URL of the small Vosk model server (e.g.
// alphacep/kaldi-ru), like "ws://127.0.0.1:2700". When set, it is used as a
// cheap wake-word gate before the heavy whisper pass.
func voskServerAddr() string {
	return strings.TrimSpace(os.Getenv("VOSK_SERVER_ADDR"))
}

// voskOnly reports whether Vosk should also produce the command text (skipping
// whisper entirely). Enabled with STT_VOSK_ONLY=1/true.
func voskOnly() bool {
	v := strings.TrimSpace(os.Getenv("STT_VOSK_ONLY"))
	return v == "1" || strings.EqualFold(v, "true")
}

// voskTranscribe runs the small Vosk model over the clip via its websocket API
// (alphacep/kaldi-ru): send a sample-rate config, stream raw 16-bit PCM, send
// EOF, then read the final result. Returns the recognized text.
func voskTranscribe(ctx context.Context, mono []int16) (string, error) {
	addr := voskServerAddr()
	if addr == "" {
		return "", fmt.Errorf("VOSK_SERVER_ADDR is not set")
	}

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, addr, nil)
	if err != nil {
		return "", fmt.Errorf("vosk dial: %w", err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetWriteDeadline(dl)
		_ = conn.SetReadDeadline(dl)
	}

	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"config":{"sample_rate":16000}}`)); err != nil {
		return "", err
	}

	// Vosk wants raw 16-bit little-endian mono PCM (no WAV header), chunked.
	pcm := make([]byte, len(mono)*2)
	for i, s := range mono {
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(s))
	}
	const chunk = 8000
	for off := 0; off < len(pcm); off += chunk {
		end := off + chunk
		if end > len(pcm) {
			end = len(pcm)
		}
		if err := conn.WriteMessage(websocket.BinaryMessage, pcm[off:end]); err != nil {
			return "", err
		}
	}
	// vosk-server compares this end-of-stream marker with an exact string
	// (including the spaces around the colon), so it must match verbatim.
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"eof" : 1}`)); err != nil {
		return "", err
	}

	// Read until the final result (a message carrying a "text" field, as opposed
	// to interim "partial" messages).
	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return "", err
		}
		if bytes.Contains(msg, []byte(`"text"`)) {
			var r struct {
				Text string `json:"text"`
			}
			if err := json.Unmarshal(msg, &r); err != nil {
				return "", err
			}
			return r.Text, nil
		}
	}
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

// stripWakeWord returns the command that follows the first wake word in text
// (e.g. "Марина, включи музыку" -> "включи музыку"). ok is false if no wake word
// is present. The returned command is trimmed and may be empty (just the name).
func stripWakeWord(text string) (command string, ok bool) {
	words := strings.Fields(text)
	for idx, w := range words {
		nw := strings.TrimSpace(normalize(w))
		for _, wake := range wakeWords {
			if strings.Contains(nw, wake) {
				return strings.TrimSpace(strings.Join(words[idx+1:], " ")), true
			}
		}
	}
	return "", false
}

// transcribe turns 16kHz-mono PCM into text by posting it to the whisper HTTP
// service (onerahmet/openai-whisper-asr-webservice): POST /asr with the WAV as
// the "audio_file" multipart field; output=txt returns the plain transcript.
// encode=false because we already send 16kHz-mono WAV (skips server-side ffmpeg).
func transcribe(ctx context.Context, mono []int16) (string, error) {
	addr := whisperServerAddr()
	if addr == "" {
		return "", fmt.Errorf("WHISPER_SERVER_ADDR is not set")
	}

	wav := pcmToWAV(mono, sttSampleRate, 1)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("audio_file", "utt.wav")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(wav); err != nil {
		return "", err
	}
	if err := mw.Close(); err != nil {
		return "", err
	}

	url := strings.TrimRight(addr, "/") + "/asr?task=transcribe&language=ru&output=txt&encode=false"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("whisper server request: %w", err)
	}
	defer resp.Body.Close()

	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("whisper server %s: %s", resp.Status, strings.TrimSpace(string(out)))
	}
	return cleanWhisperOutput(string(out)), nil
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
