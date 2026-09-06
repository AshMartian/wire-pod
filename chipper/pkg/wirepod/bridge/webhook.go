package bridge

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/wirepod/sdkapp"
)

const hermesWebhookEnv = "WIREPOD_HERMES_WEBHOOKS"

type webhookTarget struct {
	URL    string `json:"url"`
	Secret string `json:"secret"`
}

type webhookForwarder struct {
	target  webhookTarget
	queue   chan sdkapp.HermesRobotEvent
	client  *http.Client
	prepare func(sdkapp.HermesRobotEvent) sdkapp.HermesRobotEvent
}

func webhookTargetsFromEnv(value string) (map[string]webhookTarget, error) {
	if strings.TrimSpace(value) == "" {
		return map[string]webhookTarget{}, nil
	}
	var rawTargets map[string]webhookTarget
	if err := json.Unmarshal([]byte(value), &rawTargets); err != nil || len(rawTargets) == 0 {
		return nil, os.ErrInvalid
	}
	targets := make(map[string]webhookTarget, len(rawTargets))
	for esn, target := range rawTargets {
		esn = strings.ToLower(strings.TrimSpace(esn))
		parsed, err := url.Parse(target.URL)
		if esn == "" || err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || len(strings.TrimSpace(target.Secret)) < 32 {
			return nil, os.ErrInvalid
		}
		if _, duplicate := targets[esn]; duplicate {
			return nil, os.ErrInvalid
		}
		target.URL = strings.TrimRight(target.URL, "/")
		target.Secret = strings.TrimSpace(target.Secret)
		targets[esn] = target
	}
	return targets, nil
}

func newWebhookForwarder(target webhookTarget, prepare func(sdkapp.HermesRobotEvent) sdkapp.HermesRobotEvent) *webhookForwarder {
	forwarder := &webhookForwarder{
		target:  target,
		queue:   make(chan sdkapp.HermesRobotEvent, 32),
		client:  &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
		prepare: prepare,
	}
	go forwarder.run()
	return forwarder
}

func (f *webhookForwarder) enqueue(event sdkapp.HermesRobotEvent) {
	select {
	case f.queue <- event:
	default:
		logger.Println("Hermes webhook queue is full; dropping a coalesced robot event")
	}
}

func (f *webhookForwarder) run() {
	for event := range f.queue {
		f.deliver(event)
	}
}

func (f *webhookForwarder) deliver(event sdkapp.HermesRobotEvent) {
	if f.prepare != nil {
		event = f.prepare(event)
	}
	body, err := json.Marshal(event)
	if err != nil {
		return
	}
	timestamp := time.Now().Unix()
	signed := []byte(stringifyTimestamp(timestamp) + ".")
	signed = append(signed, body...)
	mac := hmac.New(sha256.New, []byte(f.target.Secret))
	_, _ = mac.Write(signed)
	deliveryID := make([]byte, 16)
	if _, err := rand.Read(deliveryID); err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, f.target.URL, bytes.NewReader(body))
	if err != nil {
		return
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-GitHub-Event", event.Type)
	request.Header.Set("X-GitHub-Delivery", hex.EncodeToString(deliveryID))
	request.Header.Set("X-Webhook-Timestamp", stringifyTimestamp(timestamp))
	request.Header.Set("X-Webhook-Signature-V2", hex.EncodeToString(mac.Sum(nil)))
	response, err := f.client.Do(request)
	if err == nil && response != nil {
		response.Body.Close()
	}
}

func stringifyTimestamp(timestamp int64) string {
	return strconv.FormatInt(timestamp, 10)
}

func (s *Server) startEventForwarding() {
	targets, err := webhookTargetsFromEnv(os.Getenv(hermesWebhookEnv))
	if err != nil || len(targets) == 0 {
		return
	}
	autonomy, err := autonomyTargetsFromEnv(os.Getenv(hermesAutonomyEnv))
	if err != nil {
		autonomy = map[string]sdkapp.HermesAutonomyConfig{}
	}
	for _, credential := range s.credentials {
		target, ok := targets[strings.ToLower(credential.esn)]
		if !ok {
			continue
		}
		forwarder := newWebhookForwarder(target, s.attachEventSnapshot)
		sdkapp.StartHermesEvents(credential.esn, forwarder.enqueue)
		if config, ok := autonomy[strings.ToLower(credential.esn)]; ok {
			sdkapp.StartHermesAutonomy(credential.esn, config, forwarder.enqueue)
		}
	}
}
