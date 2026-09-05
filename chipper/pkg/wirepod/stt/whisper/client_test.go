package wirepod_whisper

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	pb "github.com/digital-dream-labs/api/go/chipperpb"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	sr "github.com/kercre123/wire-pod/chipper/pkg/wirepod/speechrequest"
)

func TestTranscriptionEndpoint(t *testing.T) {
	for _, test := range []struct{ host, want string }{
		{"http://localhost:8000", "http://localhost:8000/v1/audio/transcriptions"},
		{"http://localhost:8000/v1/", "http://localhost:8000/v1/audio/transcriptions"},
		{"https://stt.example/proxy/v1", "https://stt.example/proxy/v1/audio/transcriptions"},
		{"https://stt.example/proxy", "https://stt.example/proxy/v1/audio/transcriptions"},
		{"http://[::1]:8000/v1/audio/transcriptions/", "http://[::1]:8000/v1/audio/transcriptions"},
	} {
		got, err := transcriptionEndpoint(test.host)
		if err != nil || got != test.want {
			t.Errorf("endpoint(%q) = %q, %v; want %q", test.host, got, err, test.want)
		}
	}
	for _, host := range []string{"localhost:8000", "ftp://localhost", "http:///v1", "http://user:secret@localhost", "http://localhost?key=secret", "http://localhost#secret", "http://localhost:bad", "http://localhost?"} {
		if _, err := transcriptionEndpoint(host); err == nil {
			t.Errorf("accepted invalid endpoint %q", host)
		}
	}
}

func TestCredentialsAndConfig(t *testing.T) {
	t.Setenv("OPENAI_KEY", "private-cloud-key")
	t.Setenv("STT_HOST", "http://localhost:8000/v1")
	t.Setenv("STT_KEY", "")
	t.Setenv("STT_MODEL", "base.en")
	t.Setenv("STT_TIMEOUT", "15s")
	cfg, err := loadTranscriptionConfig()
	if err != nil || cfg.key != "" || cfg.model != "base.en" || cfg.timeout != 15*time.Second {
		t.Fatalf("custom config: %+v, %v", cfg, err)
	}
	t.Setenv("STT_KEY", "custom-key")
	cfg, err = loadTranscriptionConfig()
	if err != nil || cfg.key != "custom-key" {
		t.Fatal("custom credential not selected")
	}
	t.Setenv("STT_HOST", "")
	cfg, err = loadTranscriptionConfig()
	if err != nil || cfg.key != "private-cloud-key" || cfg.endpoint != "https://api.openai.com/v1/audio/transcriptions" {
		t.Fatal("cloud configuration not preserved")
	}
	t.Setenv("OPENAI_KEY", "")
	if Init() == nil {
		t.Error("missing cloud key accepted")
	}
	t.Setenv("STT_HOST", "http://localhost")
	for _, timeout := range []string{"0s", "-1s", "forever"} {
		t.Setenv("STT_TIMEOUT", timeout)
		if Init() == nil {
			t.Errorf("invalid timeout %q accepted", timeout)
		}
	}
}

func TestMultipartPreservesLanguagePromptAndWAV(t *testing.T) {
	oldLanguage, oldIntents := vars.APIConfig.STT.Language, vars.IntentList
	t.Cleanup(func() { vars.APIConfig.STT.Language, vars.IntentList = oldLanguage, oldIntents })
	vars.APIConfig.STT.Language = "de-DE"
	vars.IntentList = []vars.JsonIntent{{Keyphrases: []string{"hi", "Fahre vorwärts"}}}
	pcm := []byte{0, 0, 1, 0, 255, 255}
	wavData, err := pcm2wav(bytes.NewReader(pcm))
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/audio/transcriptions" {
			t.Error("wrong method/path")
		}
		if got := r.Header.Get("Authorization"); got != "" {
			t.Errorf("leaked cloud credential: header must be absent")
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Error(err)
			http.Error(w, "bad form", 400)
			return
		}
		defer r.MultipartForm.RemoveAll()
		for key, want := range map[string]string{"model": "local-whisper", "language": "de", "prompt": "Fahre vorwärts. "} {
			if got := r.FormValue(key); got != want {
				t.Errorf("%s = %q, want %q", key, got, want)
			}
		}
		file, header, err := r.FormFile("file")
		if err != nil {
			t.Error(err)
			return
		}
		defer file.Close()
		got, err := io.ReadAll(file)
		if err != nil || header.Filename != "audio.wav" || !bytes.Equal(got, wavData) || string(got[:4]) != "RIFF" {
			t.Error("audio payload is not the encoded WAV")
		}
		io.WriteString(w, `{"text":"Fahre vorwärts","extra":"allowed"}`)
	}))
	defer server.Close()
	t.Setenv("STT_HOST", server.URL+"/v1/")
	t.Setenv("STT_MODEL", "local-whisper")
	t.Setenv("STT_KEY", "")
	t.Setenv("OPENAI_KEY", "must-not-leak")
	t.Setenv("STT_TIMEOUT", "2s")
	got, err := makeOpenAIReq(context.Background(), wavData)
	if err != nil || got != "Fahre vorwärts" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestTranscriptionResponseErrors(t *testing.T) {
	for _, test := range []struct {
		name    string
		status  int
		body    string
		wantErr bool
	}{
		{"success", 200, `{"text":"hello"}`, false},
		{"silence", 200, `{"text":""}`, false},
		{"unauthorized", 401, `{"error":"private-marker"}`, true},
		{"unavailable", 503, "private-marker", true},
		{"malformed", 200, "private-marker", true},
		{"missing", 200, `{"error":"private-marker"}`, true},
		{"wrong type", 200, `{"text":42}`, true},
		{"null", 200, `{"text":null}`, true},
		{"multiple documents", 200, `{"text":"hi"}{"text":"bye"}`, true},
		{"oversized", 200, strings.Repeat("x", maxTranscriptionResponse+1), true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer custom-key" {
					t.Error("custom key missing")
				}
				w.WriteHeader(test.status)
				io.WriteString(w, test.body)
			}))
			defer server.Close()
			_, err := transcribe(context.Background(), transcriptionConfig{endpoint: server.URL, model: "test", key: "custom-key", timeout: time.Second}, nil, "", "")
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, test.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), "private-marker") {
				t.Error("response content leaked in error")
			}
		})
	}
}

func TestTranscriptionCancellationAndTimeout(t *testing.T) {
	for _, duringBody := range []bool{false, true} {
		for _, cancelParent := range []bool{false, true} {
			t.Run(fmtTestName(duringBody, cancelParent), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					io.Copy(io.Discard, r.Body)
					if duringBody {
						w.WriteHeader(200)
						w.(http.Flusher).Flush()
					}
					if cancelParent {
						cancel()
					}
					<-r.Context().Done()
				}))
				defer server.Close()
				_, err := transcribe(ctx, transcriptionConfig{endpoint: server.URL, timeout: 100 * time.Millisecond}, nil, "", "")
				want := context.DeadlineExceeded
				if cancelParent {
					want = context.Canceled
				}
				if !errors.Is(err, want) {
					t.Fatalf("got %v, want %v", err, want)
				}
			})
		}
	}
}

func fmtTestName(body, cancel bool) string {
	name := "headers"
	if body {
		name = "body"
	}
	if cancel {
		return name + "/canceled"
	}
	return name + "/timeout"
}

func TestRedirectDoesNotForwardAudioOrKey(t *testing.T) {
	var hits int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { atomic.AddInt32(&hits, 1) }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	_, err := transcribe(context.Background(), transcriptionConfig{endpoint: origin.URL, key: "secret", timeout: time.Second}, []byte("audio"), "", "")
	if err == nil || atomic.LoadInt32(&hits) != 0 {
		t.Fatalf("redirect followed or accepted: %v", err)
	}
}

func TestMalformedPCMReturnsError(t *testing.T) {
	if _, err := pcm2wav(bytes.NewReader([]byte{1})); err == nil {
		t.Error("partial PCM sample accepted")
	}
}

type eofIntentStream struct {
	pb.ChipperGrpc_StreamingIntentServer
	ctx context.Context
}

func (s eofIntentStream) Context() context.Context                  { return s.ctx }
func (s eofIntentStream) Recv() (*pb.StreamingIntentRequest, error) { return nil, io.EOF }

func TestSTTEOFAndStreamCancellation(t *testing.T) {
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		io.WriteString(w, `{"text":"HALLO"}`)
	}))
	defer server.Close()
	t.Setenv("STT_HOST", server.URL)
	t.Setenv("STT_KEY", "")
	t.Setenv("STT_MODEL", "test")
	t.Setenv("STT_TIMEOUT", "2s")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request := sr.SpeechRequest{Stream: eofIntentStream{ctx: ctx}, DecodedMicData: []byte{0, 0, 1, 0}}
	got, err := STT(request)
	if err != nil || got != "hallo" {
		t.Fatalf("EOF with audio: %q, %v", got, err)
	}
	cancel()
	if _, err := STT(request); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost gRPC cancellation: %v", err)
	}
	request.DecodedMicData = nil
	if got, err := STT(request); err != nil || got != "" {
		t.Fatalf("empty stream: %q, %v", got, err)
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("empty/canceled stream sent audio: %d requests", got)
	}
}
