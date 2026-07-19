package voice

import (
	"bytes"
	"context"
	"discordAudio/internal/logger"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/gorilla/websocket"
)

// Discord delivers 48kHz stereo; the Vosk gate's protocol wants 16kHz mono.
const (
	recvSampleRate = 48000
	recvChannels   = 2
	sttSampleRate  = 16000
)

// wakeWords are normalized (lowercase, ё→е) trigger phrases. The bot treats an
// utterance as an AI command only if it contains one of these. Matching is
// substring-based, so "марин" also catches марину/марине/маринка etc.
//
// Overridable with WAKE_WORDS (comma-separated). Renaming the bot means setting
// WAKE_WORDS, WAKE_WORDS_NEAR and STT_PROMPT together — the two lists have
// opposite jobs, see nearWakeWords.
var defaultWakeWords = []string{
	"марин", "марина", "маринка", "мариночка",
	"мариш", "мариша", "маришка",
	"marina", "marin",
}

func wakeWords() []string { return wordListEnv("WAKE_WORDS", defaultWakeWords) }

// nearWakeWords is the LOOSE first stage, matched against the Vosk gate only.
// It deliberately contains real words that are not the wake word: the big Vosk
// model has an open vocabulary, and its language model prefers "машина" (common
// noun) over "марина" (a name) on near-identical acoustics — that substitution
// is exactly why the bot ignored people saying "Марина, как дела".
//
// Precision is explicitly NOT this list's job. Everything it nominates is
// re-checked against the accurate transcript with containsWakeWord, so a false
// alarm costs one transcription (~$0.0002) and never a wrong action. Keep this
// list generous and the strict list above honest.
//
// Do not "fix" this by adding these words to wakeWords instead: "включи Машину
// времени" would then wake the bot for real.
//
// Overridable with WAKE_WORDS_NEAR (comma-separated). Fill it with how the cheap
// model *mishears* the name, not with the name's own forms — copying WAKE_WORDS
// into it brings back the bug this two-list design exists to fix.
var defaultNearWakeWords = []string{
	"мари", "машин", "малин", "морин", "марьин", "мурин",
	"marin", "machin",
}

func nearWakeWords() []string { return wordListEnv("WAKE_WORDS_NEAR", defaultNearWakeWords) }

// wordListEnv reads a comma-separated list of wake words, normalized the same
// way transcripts are (lowercase, ё→е) so an entry typed as "Марина" in .env
// still matches.
//
// An empty or all-blank value falls back to def rather than yielding an empty
// list: a stray "WAKE_WORDS=" would otherwise leave a bot that never answers to
// anything, and nothing in the logs would say why.
func wordListEnv(name string, def []string) []string {
	raw := os.Getenv(name)
	if strings.TrimSpace(raw) == "" {
		return def
	}
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if w := strings.TrimSpace(normalize(part)); w != "" {
			out = append(out, w)
		}
	}
	if len(out) == 0 {
		log.Printf("[stt] %s has no usable words, keeping the defaults", name)
		return def
	}
	return out
}

// CheckWakeWordConfig reports wake-word settings that would leave the bot deaf,
// at startup rather than in the middle of a party.
//
// The trap this exists for: WAKE_WORDS is renamed and WAKE_WORDS_NEAR is left at
// its defaults. Stage 1 then nominates only mishearings of the *old* name, so
// nothing ever reaches the strict check and the bot never answers — while every
// list involved looks perfectly reasonable on its own.
func CheckWakeWordConfig() {
	for _, w := range wakeWords() {
		if containsNearWake(w) {
			return
		}
	}
	logger.Errorf("[stt] no wake word in WAKE_WORDS is matched by WAKE_WORDS_NEAR (%v vs %v): "+
		"the cheap gate will never nominate an utterance and the bot will not respond to its name",
		wakeWords(), nearWakeWords())
}

// containsNearWake reports whether the cheap gate heard anything close enough to
// the wake word to be worth spending the accurate model on.
func containsNearWake(text string) bool {
	n := normalize(text)
	for _, w := range nearWakeWords() {
		if strings.Contains(n, w) {
			return true
		}
	}
	return false
}

// STT log verbosity levels, selected via the STT_LOG_LEVEL env var.
const (
	sttLogSilent   = 0 // nothing
	sttLogCommands = 1 // only wake-word commands (text after "Марина") + AI round-trip
	sttLogAll      = 2 // every transcribed utterance, gate included
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

// voskServerAddr is the websocket URL of the small Vosk model server (e.g.
// alphacep/kaldi-ru), like "ws://127.0.0.1:2700". When set, it is used as a
// cheap wake-word gate before paying for the accurate model.
func voskServerAddr() string {
	return strings.TrimSpace(os.Getenv("VOSK_SERVER_ADDR"))
}

// voskOnly reports whether Vosk should also produce the command text (skipping
// the command model entirely). Enabled with STT_VOSK_ONLY=1/true. This is a
// fallback: Vosk's small model is a wake-word gate, and leaning on it for the
// command text is why recognition felt broken.
func voskOnly() bool {
	v := strings.TrimSpace(os.Getenv("STT_VOSK_ONLY"))
	return v == "1" || strings.EqualFold(v, "true")
}

// openaiKey enables the OpenAI transcription backend when set. Only utterances
// the local Vosk gate already accepted (i.e. someone said "Марина") are ever
// sent off the machine — the gate bounds both the bill and what leaves the box.
func openaiKey() string {
	return strings.TrimSpace(os.Getenv("OPENAI_API_KEY"))
}

// openaiSTTModel overrides the transcription model via OPENAI_STT_MODEL.
// gpt-4o-mini-transcribe: ~5.3% WER on Russian (FLEURS), $0.003/min.
func openaiSTTModel() string {
	if m := strings.TrimSpace(os.Getenv("OPENAI_STT_MODEL")); m != "" {
		return m
	}
	return "gpt-4o-mini-transcribe"
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
	for _, w := range wakeWords() {
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
		for _, wake := range wakeWords() {
			if strings.Contains(nw, wake) {
				return strings.TrimSpace(strings.Join(words[idx+1:], " ")), true
			}
		}
	}
	return "", false
}

// whisperPrompt biases the decoder toward this bot's vocabulary — the wake word
// and the words used to control playback. Whisper only consumes the last 224
// tokens of the prompt and weighs later tokens more, so keep it short and put
// the domain words at the end. This is contextual biasing: it buys most of what
// "training the model on our keywords" would, for free and at inference time.
// Overridable with STT_PROMPT. Its FIRST SENTENCE doubles as the echo defence
// below, so keep it something no real user would say.
const defaultWhisperPrompt = "Разговор с ботом Мариной. Марина, включи музыку. Марина, поставь на паузу. " +
	"Марина, продолжи. Марина, сделай погромче. Марина, потише. Марина, пропусти трек. " +
	"Марина, что в очереди? Марина, останови."

func whisperPrompt() string {
	if p := strings.TrimSpace(os.Getenv("STT_PROMPT")); p != "" {
		return p
	}
	return defaultWhisperPrompt
}

// promptEcho is the prompt's first sentence, normalized for matching. Fed
// non-speech, the model parrots the prompt back as the transcript, and that echo
// carries the wake word — so it would hand the AI a phantom command.
//
// Derived rather than listed: the marker and the prompt used to be two copies of
// one sentence, and a rename that updated only the prompt would have quietly
// disarmed the defence with nothing to notice it.
func promptEcho() string {
	first, _, _ := strings.Cut(whisperPrompt(), ".")
	return strings.TrimSpace(reSpaces.ReplaceAllString(normalize(first), " "))
}

// transcribe turns a captured utterance (48kHz stereo PCM) into command text.
//
// There is no local fallback: the self-hosted whisper container was retired once
// OpenAI proved both faster and more accurate here (~5.3% WER on Russian at
// 0.5-1.5s, against 4s+ for whisper medium on this CPU — and the GPU route is
// closed, the RTX 50-series being too new for the image's PyTorch). Only
// utterances the local Vosk gate has already accepted reach this point.
func transcribe(ctx context.Context, pcm []int16) (string, error) {
	if openaiKey() == "" {
		return "", fmt.Errorf("OPENAI_API_KEY is not set: voice commands cannot be transcribed")
	}
	return openaiTranscribe(ctx, pcm)
}

// toFLAC16kMono re-encodes the captured 48k stereo PCM as 16kHz mono FLAC via
// ffmpeg. Two reasons: ffmpeg resamples properly (our own decimator aliased the
// fricative band), and FLAC is lossless but roughly an order of magnitude
// smaller than the raw WAV — the upload is on the critical path for latency.
func toFLAC16kMono(ctx context.Context, pcm []int16) ([]byte, error) {
	raw := make([]byte, len(pcm)*2)
	for i, s := range pcm {
		binary.LittleEndian.PutUint16(raw[i*2:], uint16(s))
	}

	cmd := exec.CommandContext(ctx, "ffmpeg",
		"-f", "s16le",
		"-ar", strconv.Itoa(recvSampleRate),
		"-ac", strconv.Itoa(recvChannels),
		"-i", "pipe:0",
		"-ar", strconv.Itoa(sttSampleRate),
		"-ac", "1",
		"-f", "flac",
		"pipe:1",
	)
	cmd.Stdin = bytes.NewReader(raw)

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ffmpeg flac: %w; stderr: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// openaiTranscribe posts the utterance to OpenAI's transcription API. The prompt
// carries the same contextual biasing as the local backend.
func openaiTranscribe(ctx context.Context, pcm []int16) (string, error) {
	audio, err := toFLAC16kMono(ctx, pcm)
	if err != nil {
		return "", err
	}

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "utt.flac")
	if err != nil {
		return "", err
	}
	if _, err := part.Write(audio); err != nil {
		return "", err
	}
	for field, value := range map[string]string{
		"model":           openaiSTTModel(),
		"language":        "ru",
		"prompt":          whisperPrompt(),
		"response_format": "text",
	} {
		if err := mw.WriteField(field, value); err != nil {
			return "", err
		}
	}
	if err := mw.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://api.openai.com/v1/audio/transcriptions", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+openaiKey())
	req.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("openai stt request: %w", err)
	}
	defer resp.Body.Close()

	out, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("openai stt %s: %s", resp.Status, strings.TrimSpace(string(out)))
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

// NOTE: there used to be a trimSilence() here that cut the clip down to the
// region above a fixed int16 amplitude (350). It is gone on purpose. Unvoiced
// consonants (с, ш, ф, х, ц) are inherently low-amplitude, so a flat threshold
// ate the starts and ends of words, and on a quiet mic the companion RMS gate
// discarded whole utterances. Benchmarks on real Russian audio show this kind of
// preprocessing (VAD-cut, AGC) costs 6-9 points of WER, because the models are
// trained on unprocessed audio. The gate below only rejects digital silence, and
// nothing else trims: the accurate model is sent the utterance as captured.

var (
	reBracket = regexp.MustCompile(`\[[^\]]*\]`)
	reParen   = regexp.MustCompile(`\([^)]*\)`)
	reStar    = regexp.MustCompile(`\*[^*]*\*`)
	reSpaces  = regexp.MustCompile(`\s+`)

	// Substrings that mark a hallucination rather than something a user said.
	hallucinationMarkers = []string{
		// YouTube-subtitle artifacts baked into whisper's training data.
		"редактор субтитров", "корректор", "субтитры",
		"подпишись", "продолжение следует", "спасибо за просмотр",
		"ставьте лайк", "amara", "dimatorzok",
		// Prompt echo is handled separately, by promptEcho() — see cleanTranscript.
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
	// The OpenAI API has no vad_filter, so this and the Vosk gate are the whole
	// defence against the model echoing our own prompt back at us.
	if echo := promptEcho(); echo != "" && strings.Contains(n, echo) {
		return ""
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
