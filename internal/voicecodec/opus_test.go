//go:build opus && cgo

package voicecodec

import (
	"encoding/binary"
	"math"
	"testing"
)

func TestOpusSpeechFrameRoundTrip(t *testing.T) {
	e, err := (Opus{}).Encoder()
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	input := make([]byte, 1920)
	for i := 0; i < 960; i++ {
		binary.LittleEndian.PutUint16(input[i*2:], uint16(int16(12000*math.Sin(float64(i)*2*math.Pi*440/16000))))
	}
	packet, err := e.Encode(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, rate := range []int{16000, 24000} {
		d, err := (Opus{}).Decoder(rate)
		if err != nil {
			t.Fatal(err)
		}
		pcm, err := d.Decode(packet)
		if err != nil {
			t.Fatal(err)
		}
		if len(pcm) != rate*60/1000*2 {
			t.Fatalf("wrong sample count at %d Hz: %d", rate, len(pcm))
		}
		var energy float64
		for i := 0; i < len(pcm); i += 2 {
			v := float64(int16(binary.LittleEndian.Uint16(pcm[i:])))
			energy += v * v
		}
		if energy/float64(len(pcm)/2) < 1e6 {
			t.Fatal("decoded audio is unexpectedly silent")
		}
		if err := d.Reset(); err != nil {
			t.Fatal(err)
		}
		if _, err := d.Decode([]byte{0xff}); err == nil {
			t.Fatal("invalid Opus packet accepted")
		}
		d.Close()
	}
	if _, err := e.Encode(input[:100]); err == nil {
		t.Fatal("partial frame must be padded by caller")
	}
}

func TestOpusRejectsInvalidRateAndEmptyPacket(t *testing.T) {
	if !Available() {
		t.Fatal("native build must report available")
	}
	if _, err := (Opus{}).Decoder(12345); err == nil {
		t.Fatal("invalid sample rate accepted")
	}
	d, err := (Opus{}).Decoder(16000)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Decode(nil); err == nil {
		t.Fatal("empty packet accepted")
	}
	d.Close() // Releasing a codec is safe more than once.
	e, err := (Opus{}).Encoder()
	if err != nil {
		t.Fatal(err)
	}
	e.Close()
	e.Close()
}
