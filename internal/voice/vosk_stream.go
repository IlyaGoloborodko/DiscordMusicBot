package voice

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// voskStreaming enables the persistent-connection gate (STT_VOSK_STREAM=1).
//
// The default path opens a websocket per utterance and only knows an utterance
// ended because our own timer said so. Streaming instead keeps one connection
// per speaker and lets Kaldi decide, which removes two costs: the fixed pause we
// wait out on every command, and the hard segment cap that can cut a wake word
// in half so neither piece contains it.
func voskStreaming() bool {
	v := strings.TrimSpace(os.Getenv("STT_VOSK_STREAM"))
	return v == "1" || strings.EqualFold(v, "true")
}

// voskStream is one speaker's live connection to the Vosk server. Audio is
// written as it arrives and the server answers continuously, so by the time an
// utterance ends its transcript is already in hand — the gate no longer costs a
// round trip on the critical path.
//
// Utterance boundaries come from the caller's timer, not from Kaldi's
// endpointer. Measured against the live server: the endpointer needs a real
// noise floor to fire and stays silent through digital zeros, and Discord simply
// stops sending packets when nobody speaks. Waiting for a final would mean
// waiting forever in exactly the common case.
type voskStream struct {
	conn *websocket.Conn

	wmu    sync.Mutex // serialises writes; gorilla allows one writer at a time
	closed bool

	tmu     sync.Mutex
	final   []string // utterances Kaldi closed on its own, if it managed to
	partial string   // the in-progress transcript

	finalised chan struct{} // signalled when a final result lands, for Flush
	done      chan struct{}
	once      sync.Once
}

// signalFinal wakes a waiting Flush without blocking the reader if none is.
func (s *voskStream) signalFinal() {
	select {
	case s.finalised <- struct{}{}:
	default:
	}
}

// Flush ends the utterance and waits for the complete transcript.
//
// The decoder holds words back until audio follows them, and Discord sends
// nothing once a speaker stops — so without this the tail of an utterance, and
// for a short one every word of it, never appears. Measured: a 1.4s "Марина"
// came back empty, while the same audio through the per-utterance path was
// recognised. Padding with noise does not fix it; the end-of-stream marker does,
// because it is what makes the server produce its final result.
//
// The marker also ends the session, which is why the caller recycles the
// connection afterwards.
func (s *voskStream) Flush() {
	s.wmu.Lock()
	if s.closed {
		s.wmu.Unlock()
		return
	}
	_ = s.conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	err := s.conn.WriteMessage(websocket.TextMessage, []byte(voskEOF))
	s.wmu.Unlock()
	if err != nil {
		return
	}

	// Cap the wait so a wedged server delays a command rather than losing it.
	select {
	case <-s.finalised:
	case <-s.done:
	case <-time.After(2 * time.Second):
	}
}

// Peek reports what has been decoded so far without consuming it or ending the
// utterance, for deciding whether the audio held so far is worth keeping.
func (s *voskStream) Peek() string {
	s.tmu.Lock()
	defer s.tmu.Unlock()
	return strings.TrimSpace(strings.Join(append(append([]string(nil), s.final...), s.partial), " "))
}

// TakeText returns everything heard since the last call and clears it. The
// connection must be recycled afterwards: the recognizer keeps its decoder state
// otherwise, and the next utterance would arrive with this one still glued to
// the front — one "Марина" would then wake the bot on every sentence after it.
func (s *voskStream) TakeText() string {
	s.tmu.Lock()
	defer s.tmu.Unlock()

	parts := append([]string(nil), s.final...)
	if s.partial != "" {
		parts = append(parts, s.partial)
	}
	s.final, s.partial = nil, ""
	return strings.TrimSpace(strings.Join(parts, " "))
}

type voskResult struct {
	Text    string `json:"text"`
	Partial string `json:"partial"`
}

// dialVoskStream opens the connection and starts reading. The callbacks are
// optional and used by tests; normal callers poll TakeText at utterance end.
func dialVoskStream(ctx context.Context, onFinal, onPartial func(string)) (*voskStream, error) {
	addr := voskServerAddr()
	if addr == "" {
		return nil, fmt.Errorf("VOSK_SERVER_ADDR is not set")
	}

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, addr, nil)
	if err != nil {
		return nil, fmt.Errorf("vosk dial: %w", err)
	}
	if err := conn.WriteMessage(websocket.TextMessage, []byte(`{"config":{"sample_rate":16000}}`)); err != nil {
		conn.Close()
		return nil, fmt.Errorf("vosk config: %w", err)
	}

	s := &voskStream{
		conn:      conn,
		done:      make(chan struct{}),
		finalised: make(chan struct{}, 1),
	}
	go s.read(onFinal, onPartial)
	return s, nil
}

func (s *voskStream) read(onFinal, onPartial func(string)) {
	defer s.Close()
	for {
		_, msg, err := s.conn.ReadMessage()
		if err != nil {
			select {
			case <-s.done: // closed by us, not an error
			default:
				if sttLogLevel() >= sttLogCommands {
					log.Println("[stt] vosk stream read:", err)
				}
			}
			return
		}

		var r voskResult
		if err := json.Unmarshal(msg, &r); err != nil {
			continue
		}
		// A "text" field marks an utterance Kaldi closed by itself; "partial" is
		// the running transcript of the one still in progress.
		if bytes.Contains(msg, []byte(`"text"`)) {
			s.tmu.Lock()
			if r.Text != "" {
				s.final = append(s.final, r.Text)
			}
			s.partial = "" // the recognizer reset itself
			s.tmu.Unlock()
			s.signalFinal()
			if onFinal != nil {
				onFinal(r.Text)
			}
			continue
		}

		s.tmu.Lock()
		s.partial = r.Partial
		s.tmu.Unlock()
		if onPartial != nil && r.Partial != "" {
			onPartial(r.Partial)
		}
	}
}

// Write sends 16kHz mono PCM. Safe to call from the receive loop only; writes
// are serialised but ordering matters, so there should be one writer.
func (s *voskStream) Write(mono []int16) error {
	if len(mono) == 0 {
		return nil
	}
	raw := make([]byte, len(mono)*2)
	for i, v := range mono {
		binary.LittleEndian.PutUint16(raw[i*2:], uint16(v))
	}

	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.closed {
		return fmt.Errorf("vosk stream closed")
	}
	_ = s.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return s.conn.WriteMessage(websocket.BinaryMessage, raw)
}

// voskEOF is the end-of-stream marker. vosk-server compares it against this
// exact string, spaces around the colon included, so it must match verbatim.
const voskEOF = `{"eof" : 1}`

// Close drops the connection. Callers that want the final transcript call Flush
// first; Close alone does not wait for one.
func (s *voskStream) Close() {
	s.once.Do(func() {
		close(s.done)

		s.wmu.Lock()
		s.closed = true
		s.wmu.Unlock()

		_ = s.conn.Close()
	})
}
