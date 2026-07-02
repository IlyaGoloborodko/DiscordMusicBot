package discordgo

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/binary"
	"fmt"
	"strconv"
)

// DecryptFrame reverses EncryptFrame for a frame received from userID.
//
// DAVE is group E2EE: every member derives per-sender keys from the shared MLS
// exporter secret, so with exporterSecret + the sender's userID we can decrypt
// that sender's audio. The wire layout (see encryptSecureFrame) is:
//
//	[ciphertext][8-byte truncated tag][ULEB128 nonce][supplementalSize byte][0xFA 0xFA]
//
// For the Opus audio codec there are no "unencrypted ranges", so the whole
// leading region is ciphertext. We decrypt via AES-CTR (the keystream GCM uses)
// and skip tag verification — this is for transcription, not authentication, and
// Go's GCM refuses the 8-byte truncated tag anyway.
//
// Frames without the 0xFAFA marker are DAVE passthrough (plain Opus) and are
// returned unchanged.
func (d *DAVESession) DecryptFrame(userID string, frame []byte) ([]byte, error) {
	// Passthrough / not an E2EE frame.
	if len(frame) < 4 || frame[len(frame)-1] != 0xFA || frame[len(frame)-2] != 0xFA {
		return frame, nil
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if d.exporterSecret == nil {
		return nil, fmt.Errorf("dave: no exporter secret yet")
	}
	if userID == "" {
		return nil, fmt.Errorf("dave: unknown sender for encrypted frame")
	}

	// supplementalSize covers: tag + nonce + unencryptedRanges + sizeByte + marker.
	supSize := int(frame[len(frame)-3])
	minSup := daveTagSize + 1 /*min ULEB128*/ + 1 /*sizeByte*/ + 2 /*marker*/
	if supSize < minSup || supSize > len(frame) {
		return nil, fmt.Errorf("dave: bad supplemental size %d (frame %d)", supSize, len(frame))
	}

	supStart := len(frame) - supSize
	p := supStart + daveTagSize // skip the (truncated) auth tag

	nonce, _, err := readULEB128(frame[p : len(frame)-3])
	if err != nil {
		return nil, fmt.Errorf("dave: nonce: %w", err)
	}
	// Bytes between the nonce and the size byte would be serialized unencrypted
	// ranges; for Opus there are none, so we ignore them.

	ciphertext := frame[:supStart]

	generation := nonce >> 24
	key, err := d.recvKeyLocked(userID, generation)
	if err != nil {
		return nil, err
	}

	plain, err := daveCTRDecrypt(key, nonce, ciphertext)
	if err != nil {
		return nil, err
	}
	return plain, nil
}

// recvKeyLocked returns the sender's hash-ratchet key for the given generation,
// mirroring deriveSenderKeyLocked/EncryptFrame but for another member's userID.
// d.mu must be held.
func (d *DAVESession) recvKeyLocked(userID string, generation uint32) ([]byte, error) {
	base := d.recvBase[userID]
	if base == nil {
		uid, err := strconv.ParseUint(userID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("dave: parsing sender userID %q: %w", userID, err)
		}
		context := make([]byte, 8)
		binary.LittleEndian.PutUint64(context, uid)

		base, err = mlsExport(d.exporterSecret, daveExportLabel, context, daveKeySize)
		if err != nil {
			return nil, fmt.Errorf("dave: exporting base secret: %w", err)
		}
		if d.recvBase == nil {
			d.recvBase = make(map[string][]byte)
		}
		d.recvBase[userID] = base
	}
	return hashRatchetGetKey(base, generation)
}

// daveCTRDecrypt recovers the plaintext from a truncated-tag AES-GCM frame using
// only the GCM keystream (AES-CTR), without verifying the tag. For a 96-bit
// nonce, GCM's J0 = nonce || 0x00000001 and data blocks start at the next
// counter value (2), so we seed CTR with nonce||BE32(2).
func daveCTRDecrypt(key []byte, nonce uint32, ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	iv := make([]byte, 16)
	copy(iv[:12], buildNonce(nonce)) // 12-byte GCM IV (counter in LE at [8:12])
	binary.BigEndian.PutUint32(iv[12:], 2)

	out := make([]byte, len(ciphertext))
	cipher.NewCTR(block, iv).XORKeyStream(out, ciphertext)
	return out, nil
}

// readULEB128 decodes an unsigned LEB128 value, returning the value and the
// number of bytes consumed.
func readULEB128(b []byte) (uint32, int, error) {
	var result uint32
	var shift uint
	for i, x := range b {
		result |= uint32(x&0x7F) << shift
		if x&0x80 == 0 {
			return result, i + 1, nil
		}
		shift += 7
		if shift >= 32 {
			return 0, 0, fmt.Errorf("dave: ULEB128 overflow")
		}
	}
	return 0, 0, fmt.Errorf("dave: truncated ULEB128")
}
