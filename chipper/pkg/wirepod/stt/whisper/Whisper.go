package wirepod_whisper

import (
	"bytes"
	"context"
	"encoding/binary"
	"io"
	"strings"

	"github.com/go-audio/audio"
	"github.com/go-audio/wav"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	sr "github.com/kercre123/wire-pod/chipper/pkg/wirepod/speechrequest"
	"github.com/orcaman/writerseeker"
)

var Name string = "whisper"

func Init() error {
	_, err := loadTranscriptionConfig()
	return err
}

func pcm2wav(in io.Reader) ([]byte, error) {

	// Output file.
	out := &writerseeker.WriterSeeker{}

	// 16 kHz, 16 bit, 1 channel, WAV.
	e := wav.NewEncoder(out, 16000, 16, 1, 1)

	// Create new audio.IntBuffer.
	audioBuf, err := newAudioIntBuffer(in)
	if err != nil {
		return nil, err
	}
	// Write buffer to output file. This writes a RIFF header and the PCM chunks from the audio.IntBuffer.
	if err := e.Write(audioBuf); err != nil {
		return nil, err
	}
	if err := e.Close(); err != nil {
		return nil, err
	}
	outBuf := new(bytes.Buffer)
	if _, err := io.Copy(outBuf, out.BytesReader()); err != nil {
		return nil, err
	}
	return outBuf.Bytes(), nil
}

func newAudioIntBuffer(r io.Reader) (*audio.IntBuffer, error) {
	buf := audio.IntBuffer{
		Format: &audio.Format{
			NumChannels: 1,
			SampleRate:  16000,
		},
	}
	for {
		var sample int16
		err := binary.Read(r, binary.LittleEndian, &sample)
		switch {
		case err == io.EOF:
			return &buf, nil
		case err != nil:
			return nil, err
		}
		buf.Data = append(buf.Data, int(sample))
	}
}

// buildVocabPrompt builds a vocabulary hint from the loaded intents. whisper-1
// accepts a prompt to bias transcription towards expected wording, which helps a
// lot with command phrases in languages other than English.
func buildVocabPrompt() string {
	var sb strings.Builder
	for _, in := range vars.IntentList {
		for _, kp := range in.Keyphrases {
			k := strings.TrimSpace(kp)
			if len(k) < 4 {
				continue
			}
			if sb.Len()+len(k)+2 > 850 {
				return sb.String()
			}
			sb.WriteString(k)
			sb.WriteString(". ")
			break
		}
	}
	return sb.String()
}

func STT(req sr.SpeechRequest) (string, error) {
	logger.Println("(Bot " + req.Device + ", Whisper) Processing...")
	speechIsDone := false
	var err error
	for {
		_, err = req.GetNextStreamChunk()
		if err != nil {
			if err == io.EOF {
				break
			}
			return "", err
		}
		// has to be split into 320 []byte chunks for VAD
		speechIsDone, _ = req.DetectEndOfSpeech()
		if speechIsDone {
			break
		}
	}

	if len(req.DecodedMicData) == 0 {
		return "", nil
	}
	pcmBuf, err := pcm2wav(bytes.NewReader(req.DecodedMicData))
	if err != nil {
		return "", err
	}

	ctx := context.Background()
	if stream, ok := req.Stream.(interface{ Context() context.Context }); ok {
		ctx = stream.Context()
	}
	transcribedText, err := makeOpenAIReq(ctx, pcmBuf)
	if err != nil {
		return "", err
	}
	transcribedText = strings.ToLower(transcribedText)
	logger.Println("Bot " + req.Device + " Transcribed text: " + transcribedText)
	return transcribedText, nil
}
