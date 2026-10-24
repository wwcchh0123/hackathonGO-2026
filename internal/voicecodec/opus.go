//go:build opus && cgo

package voicecodec

/*
#cgo pkg-config: opus
#include <opus.h>
static int set_bitrate(OpusEncoder *e) { return opus_encoder_ctl(e, OPUS_SET_BITRATE(24000)); }
static int reset_decoder(OpusDecoder *d) { return opus_decoder_ctl(d, OPUS_RESET_STATE); }
*/
import "C"

import (
	"encoding/binary"
	"fmt"
	"unsafe"
)

func Available() bool { return true }

type encoder struct{ ptr *C.OpusEncoder }
type decoder struct {
	ptr  *C.OpusDecoder
	rate int
}

func opusError(code C.int) error { return fmt.Errorf("opus: %s", C.GoString(C.opus_strerror(code))) }

func newEncoder() (Encoder, error) {
	var code C.int
	p := C.opus_encoder_create(16000, 1, C.OPUS_APPLICATION_VOIP, &code)
	if code != C.OPUS_OK {
		return nil, opusError(code)
	}
	if code = C.set_bitrate(p); code != C.OPUS_OK {
		C.opus_encoder_destroy(p)
		return nil, opusError(code)
	}
	return &encoder{p}, nil
}

func (e *encoder) Encode(pcm []byte) ([]byte, error) {
	if len(pcm) != 1920 {
		return nil, fmt.Errorf("expected 960 PCM16 samples (60 ms), got %d bytes", len(pcm))
	}
	samples := make([]int16, 960)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(pcm[2*i:]))
	}
	packet := make([]byte, 4000)
	n := C.opus_encode(e.ptr, (*C.opus_int16)(unsafe.Pointer(&samples[0])), 960, (*C.uchar)(unsafe.Pointer(&packet[0])), C.opus_int32(len(packet)))
	if n < 0 {
		return nil, opusError(n)
	}
	return packet[:int(n)], nil
}

func (e *encoder) Close() {
	if e.ptr != nil {
		C.opus_encoder_destroy(e.ptr)
		e.ptr = nil
	}
}

func newDecoder(rate int) (Decoder, error) {
	var code C.int
	p := C.opus_decoder_create(C.opus_int32(rate), 1, &code)
	if code != C.OPUS_OK {
		return nil, opusError(code)
	}
	return &decoder{p, rate}, nil
}

func (d *decoder) Decode(packet []byte) ([]byte, error) {
	if len(packet) == 0 {
		return nil, fmt.Errorf("empty opus packet")
	}
	samples := make([]int16, d.rate*120/1000)
	n := C.opus_decode(d.ptr, (*C.uchar)(unsafe.Pointer(&packet[0])), C.opus_int32(len(packet)), (*C.opus_int16)(unsafe.Pointer(&samples[0])), C.int(len(samples)), 0)
	if n < 0 {
		return nil, opusError(n)
	}
	pcm := make([]byte, 2*int(n))
	for i := 0; i < int(n); i++ {
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(samples[i]))
	}
	return pcm, nil
}

func (d *decoder) Reset() error {
	if code := C.reset_decoder(d.ptr); code != C.OPUS_OK {
		return opusError(code)
	}
	return nil
}
func (d *decoder) Close() {
	if d.ptr != nil {
		C.opus_decoder_destroy(d.ptr)
		d.ptr = nil
	}
}
