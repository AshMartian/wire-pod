package wirepod_whisper

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

const maxTranscriptionResponse = 1024 * 1024

type transcriptionConfig struct {
	endpoint string
	model    string
	key      string
	timeout  time.Duration
}

func transcriptionEndpoint(host string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(host))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return "", errors.New("STT_HOST must be an absolute HTTP(S) URL without credentials, query, or fragment")
	}
	path := strings.TrimRight(u.Path, "/")
	switch {
	case strings.HasSuffix(path, "/audio/transcriptions"):
	case strings.HasSuffix(path, "/v1"):
		path += "/audio/transcriptions"
	default:
		path += "/v1/audio/transcriptions"
	}
	u.Path, u.RawPath = path, ""
	return u.String(), nil
}

func loadTranscriptionConfig() (transcriptionConfig, error) {
	cfg := transcriptionConfig{endpoint: "https://api.openai.com/v1/audio/transcriptions", model: "whisper-1", timeout: 30 * time.Second}
	if host := strings.TrimSpace(os.Getenv("STT_HOST")); host != "" {
		var err error
		cfg.endpoint, err = transcriptionEndpoint(host)
		if err != nil {
			return cfg, err
		}
		// A custom server must never inherit the cloud provider's credential.
		cfg.key = strings.TrimSpace(os.Getenv("STT_KEY"))
	} else {
		cfg.key = strings.TrimSpace(os.Getenv("OPENAI_KEY"))
		if cfg.key == "" {
			return cfg, errors.New("OPENAI_KEY is required when STT_HOST is unset")
		}
	}
	if model := strings.TrimSpace(os.Getenv("STT_MODEL")); model != "" {
		cfg.model = model
	}
	if timeout := strings.TrimSpace(os.Getenv("STT_TIMEOUT")); timeout != "" {
		var err error
		cfg.timeout, err = time.ParseDuration(timeout)
		if err != nil || cfg.timeout <= 0 {
			return cfg, errors.New("STT_TIMEOUT must be a positive duration such as 30s")
		}
	}
	return cfg, nil
}

func makeOpenAIReq(ctx context.Context, in []byte) (string, error) {
	cfg, err := loadTranscriptionConfig()
	if err != nil {
		return "", err
	}
	return transcribe(ctx, cfg, in, strings.Split(vars.APIConfig.STT.Language, "-")[0], buildVocabPrompt())
}

func transcribe(ctx context.Context, cfg transcriptionConfig, in []byte, language, prompt string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, cfg.timeout)
	defer cancel()
	buf := new(bytes.Buffer)
	w := multipart.NewWriter(buf)
	for _, field := range [][2]string{{"model", cfg.model}, {"language", language}, {"prompt", prompt}} {
		if field[1] != "" {
			if err := w.WriteField(field[0], field[1]); err != nil {
				return "", errors.New("could not encode Whisper request")
			}
		}
	}
	sendFile, err := w.CreateFormFile("file", "audio.wav")
	if err != nil {
		return "", errors.New("could not encode Whisper audio")
	}
	if _, err = sendFile.Write(in); err != nil {
		return "", errors.New("could not encode Whisper audio")
	}
	if err = w.Close(); err != nil {
		return "", errors.New("could not finish Whisper request")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg.endpoint, buf)
	if err != nil {
		return "", errors.New("could not create Whisper request")
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	if cfg.key != "" {
		req.Header.Set("Authorization", "Bearer "+cfg.key)
	}
	// Do not forward either the audio or credentials through redirects.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("Whisper request interrupted: %w", ctx.Err())
		}
		return "", errors.New("Whisper request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("Whisper server returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxTranscriptionResponse+1))
	if err != nil {
		if ctx.Err() != nil {
			return "", fmt.Errorf("Whisper response interrupted: %w", ctx.Err())
		}
		return "", errors.New("could not read Whisper response")
	}
	if len(body) > maxTranscriptionResponse {
		return "", errors.New("Whisper response exceeds size limit")
	}
	var response struct {
		Text *string `json:"text"`
	}
	if json.Unmarshal(body, &response) != nil || response.Text == nil {
		return "", errors.New("Whisper server returned invalid transcription JSON")
	}
	return *response.Text, nil
}
