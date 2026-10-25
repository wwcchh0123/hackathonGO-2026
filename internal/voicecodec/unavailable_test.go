//go:build !opus || !cgo

package voicecodec

import "testing"

func TestUnavailableCodecFailsExplicitly(t *testing.T) {
	if Available() {
		t.Fatal("codec reported available without native support")
	}
	if _, err := (Opus{}).Encoder(); err == nil {
		t.Fatal("missing encoder accepted")
	}
	if _, err := (Opus{}).Decoder(16000); err == nil {
		t.Fatal("missing decoder accepted")
	}
}
