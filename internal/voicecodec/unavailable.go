//go:build !opus || !cgo

package voicecodec

import "errors"

func Available() bool { return false }
func newEncoder() (Encoder, error) {
	return nil, errors.New("Opus unavailable: install libopus and use make voice-build")
}
func newDecoder(int) (Decoder, error) {
	return nil, errors.New("Opus unavailable: install libopus and use make voice-build")
}
