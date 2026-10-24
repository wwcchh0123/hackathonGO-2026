// Package voicecodec keeps native Opus optional for the control service build.
package voicecodec

type Encoder interface {
	Encode(pcm []byte) ([]byte, error)
	Close()
}

type Decoder interface {
	Decode(packet []byte) ([]byte, error)
	Reset() error
	Close()
}

type Factory interface {
	Encoder() (Encoder, error)
	Decoder(sampleRate int) (Decoder, error)
}

type Opus struct{}

func (Opus) Encoder() (Encoder, error)         { return newEncoder() }
func (Opus) Decoder(rate int) (Decoder, error) { return newDecoder(rate) }
