package common

type WAVHeader struct {
	RIFF          [4]byte
	FileSize      uint32
	WAVE          [4]byte
	FMT           [4]byte
	FMTSize       uint32
	AudioFormat   uint16
	Channels      uint16
	SampleRate    uint32
	ByteRate      uint32
	BlockAlign    uint16
	BitsPerSample uint16
	Data          [4]byte
	DataSize      uint32
}
