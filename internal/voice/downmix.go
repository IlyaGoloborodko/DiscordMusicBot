package voice

// downmixer is the streaming form of downmixTo16kMono: same LEFT-channel
// decimation through the same triangular 5-tap low-pass, but fed a packet at a
// time instead of a whole utterance.
//
// It exists because the filter window reaches two samples either side of the
// sample it emits. Run naively per packet, every 20ms Opus frame boundary would
// clamp against its own edges instead of the neighbouring audio — a click every
// 20ms, right through the band the wake word is recognised in. So the tail of
// each packet is carried over and only released once the next one supplies the
// lookahead.
type downmixer struct {
	buf  []int // left-channel samples, buf[0] is absolute index base
	base int   // absolute index of buf[0]
	next int   // absolute index of the next centre sample to emit

	// odd holds a sample left over when a write ends mid-stereo-frame. Without
	// it the orphan would be dropped and every following packet read one sample
	// out of phase — the right channel silently taken for the left.
	odd    int16
	hasOdd bool
}

// at returns the left-channel sample at an absolute index, clamped at the start
// exactly as the batch version clamps (i < 0 reads sample 0).
func (d *downmixer) at(i int) int {
	if i < d.base {
		i = d.base
	}
	if j := i - d.base; j < len(d.buf) {
		return d.buf[j]
	}
	return d.buf[len(d.buf)-1]
}

// write feeds interleaved 48kHz stereo and returns whatever 16kHz mono samples
// are now complete. Samples whose filter window is not fully covered yet are
// held back until the next call.
func (d *downmixer) write(pcm []int16) []int16 {
	i := 0
	if d.hasOdd && len(pcm) > 0 {
		// The carried sample was a LEFT one; this packet supplies its RIGHT half.
		d.buf = append(d.buf, int(d.odd))
		d.hasOdd = false
		i = 1
	}
	for ; i+1 < len(pcm); i += 2 {
		d.buf = append(d.buf, int(pcm[i])) // LEFT channel only
	}
	if i < len(pcm) {
		d.odd, d.hasOdd = pcm[i], true
	}
	if len(d.buf) == 0 {
		return nil
	}

	end := d.base + len(d.buf) // absolute index one past the last known sample
	var out []int16
	// Emit only where the +2 lookahead is real data, so the window never clamps
	// against a packet edge.
	for d.next+2 < end {
		out = append(out, d.sample(d.next))
		d.next += 3
	}
	d.trim()
	return out
}

// flush emits the tail once no more audio is coming, clamping the lookahead
// against the last sample the way the batch version does at end of buffer.
func (d *downmixer) flush() []int16 {
	if d.hasOdd {
		// Stream ended mid-frame; the sample is still real audio, keep it.
		d.buf = append(d.buf, int(d.odd))
		d.hasOdd = false
	}
	end := d.base + len(d.buf)
	var out []int16
	for d.next < end {
		out = append(out, d.sample(d.next))
		d.next += 3
	}
	d.buf = nil
	d.base = d.next
	return out
}

func (d *downmixer) sample(c int) int16 {
	v := d.at(c-2) + 2*d.at(c-1) + 3*d.at(c) + 2*d.at(c+1) + d.at(c+2)
	return int16(v / 9)
}

// trim drops samples no longer reachable, keeping the two of lookbehind the
// next window still needs.
func (d *downmixer) trim() {
	keep := d.next - 2
	if keep <= d.base {
		return
	}
	cut := keep - d.base
	if cut >= len(d.buf) {
		cut = len(d.buf)
	}
	d.buf = append(d.buf[:0], d.buf[cut:]...)
	d.base = keep
}
