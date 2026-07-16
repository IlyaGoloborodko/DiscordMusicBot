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
	neturl "net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
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
var nearWakeWords = []string{
	"мари", "машин", "малин", "морин", "марьин", "мурин",
	"marin", "machin",
}

// containsNearWake reports whether the cheap gate heard anything close enough to
// the wake word to be worth spending the accurate model on.
func containsNearWake(text string) bool {
	n := normalize(text)
	for _, w := range nearWakeWords {
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

// whisperPrompt biases the decoder toward this bot's vocabulary — the wake word
// and the words used to control playback. Whisper only consumes the last 224
// tokens of the prompt and weighs later tokens more, so keep it short and put
// the domain words at the end. This is contextual biasing: it buys most of what
// "training the model on our keywords" would, for free and at inference time.
const whisperPrompt = "Разговор с ботом Мариной. Марина, включи музыку. Марина, поставь на паузу. " +
	"Марина, продолжи. Марина, сделай погромче. Марина, потише. Марина, пропусти трек. " +
	"Марина, что в очереди? Марина, останови."

// transcribe turns a captured utterance (48kHz stereo PCM) into text using
// whichever command model is configured: OpenAI when OPENAI_API_KEY is set,
// otherwise the local whisper HTTP service.
func transcribe(ctx context.Context, pcm []int16) (string, error) {
	if openaiKey() != "" {
		return openaiTranscribe(ctx, pcm)
	}
	return whisperTranscribe(ctx, pcm)
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
		"prompt":          whisperPrompt,
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

// whisperTranscribe turns 48kHz-stereo PCM into text by posting it to the local
// whisper HTTP service (onerahmet/openai-whisper-asr-webservice): POST /asr with
// the WAV as the "audio_file" multipart field; output=txt returns the transcript.
//
// We hand over the original 48k stereo and let the server's ffmpeg resample
// (encode=true). Doing it ourselves needed a cheap decimating filter that both
// rolled off the 8kHz band (~-7dB) and aliased 9-14kHz back onto 2-7kHz at only
// -12..-19dB — exactly where the fricatives live.
//
// vad_filter lets faster_whisper drop non-speech itself, which replaces the
// amplitude-threshold trimming we used to do (that shaved the quiet onsets off
// words and dropped whole clips from quiet mics). It is NOT optional while an
// initial_prompt is set: measured on this container, silence and noise with
// vad_filter=false come back as the prompt text echoed verbatim, which would
// hand the AI a phantom command. With vad_filter=true both return empty.
func whisperTranscribe(ctx context.Context, pcm []int16) (string, error) {
	addr := whisperServerAddr()
	if addr == "" {
		return "", fmt.Errorf("WHISPER_SERVER_ADDR is not set")
	}

	wav := pcmToWAV(pcm, recvSampleRate, recvChannels)

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

	q := neturl.Values{
		"task":           {"transcribe"},
		"language":       {"ru"},
		"output":         {"txt"},
		"encode":         {"true"},
		"vad_filter":     {"true"},
		"initial_prompt": {whisperPrompt},
	}
	url := strings.TrimRight(addr, "/") + "/asr?" + q.Encode()
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

// NOTE: there used to be a trimSilence() here that cut the clip down to the
// region above a fixed int16 amplitude (350). It is gone on purpose. Unvoiced
// consonants (с, ш, ф, х, ц) are inherently low-amplitude, so a flat threshold
// ate the starts and ends of words, and on a quiet mic the companion RMS gate
// discarded whole utterances. Benchmarks on real Russian audio show this kind of
// preprocessing (VAD-cut, AGC) costs 6-9 points of WER, because the models are
// trained on unprocessed audio. Silence is now handled by the server's
// vad_filter instead, and the gate below only rejects digital silence.

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
		// Prompt echo: fed non-speech, the model parrots initial_prompt back as
		// the transcript. whisperPrompt opens with this framing sentence exactly
		// so the echo is recognisable and no real user would ever utter it. The
		// local backend has vad_filter to prevent this; the OpenAI API has no
		// such knob, so this is the defence there.
		"разговор с ботом мариной",
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
