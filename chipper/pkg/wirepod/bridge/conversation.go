package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const hermesConversationEnv = "WIREPOD_HERMES_CONVERSATIONS"

type conversationTarget struct {
	URL   string `json:"url"`
	Key   string `json:"key"`
	Model string `json:"model"`
}

type conversationTargets map[string]conversationTarget

func conversationTargetsFromEnv(value string) (conversationTargets, error) {
	if strings.TrimSpace(value) == "" {
		return conversationTargets{}, nil
	}
	var rawTargets conversationTargets
	if err := json.Unmarshal([]byte(value), &rawTargets); err != nil || len(rawTargets) == 0 {
		return nil, os.ErrInvalid
	}
	targets := make(conversationTargets, len(rawTargets))
	for esn, target := range rawTargets {
		esn = strings.ToLower(strings.TrimSpace(esn))
		parsed, err := url.Parse(target.URL)
		if esn == "" || err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || len(strings.TrimSpace(target.Key)) < 32 || strings.TrimSpace(target.Model) == "" {
			return nil, os.ErrInvalid
		}
		if _, duplicate := targets[esn]; duplicate {
			return nil, os.ErrInvalid
		}
		target.URL = strings.TrimRight(target.URL, "/")
		target.Key = strings.TrimSpace(target.Key)
		target.Model = strings.TrimSpace(target.Model)
		targets[esn] = target
	}
	return targets, nil
}

// HermesConversation sends a transcribed Vector knowledge-graph request to
// its own Hermes API profile. The session ID intentionally rotates each local
// day while the stable session key keeps consented long-term memory scoped to
// the same physical robot.
func HermesConversation(ctx context.Context, esn, transcript string) (string, bool, error) {
	targets, err := conversationTargetsFromEnv(os.Getenv(hermesConversationEnv))
	if err != nil || len(targets) == 0 {
		return "", false, nil
	}
	target, ok := targets[strings.ToLower(strings.TrimSpace(esn))]
	if !ok {
		return "", false, nil
	}
	transcript = strings.TrimSpace(transcript)
	if transcript == "" || len([]rune(transcript)) > 2000 {
		return "", true, errors.New("invalid Vector transcript")
	}
	payload, err := json.Marshal(struct {
		Model    string `json:"model"`
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}{
		Model: target.Model,
		Messages: []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{
			{Role: "system", Content: "You are replying through one Vector's speaker. Be concise, warm, and truthful. Preserve its separate identity and consent-sensitive memory rules. WirePod will speak your final text, so do not call robot speech or motion tools unless the user explicitly asks for an embodied action."},
			{Role: "user", Content: transcript},
		},
	})
	if err != nil {
		return "", true, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.URL+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", true, err
	}
	cleanESN := strings.ToLower(strings.TrimSpace(esn))
	request.Header.Set("Authorization", "Bearer "+target.Key)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Hermes-Session-Id", dailySessionID(cleanESN, time.Now()))
	request.Header.Set("X-Hermes-Session-Key", "wirepod:vector:"+cleanESN)
	client := &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return "", true, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", true, errors.New("Hermes conversation request failed")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 128*1024+1))
	if err != nil || len(body) > 128*1024 {
		return "", true, errors.New("invalid Hermes conversation response")
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &result); err != nil || len(result.Choices) == 0 {
		return "", true, errors.New("invalid Hermes conversation response")
	}
	answer := strings.TrimSpace(result.Choices[0].Message.Content)
	if answer == "" {
		return "", true, errors.New("empty Hermes conversation response")
	}
	return truncateRunes(answer, 480), true, nil
}

// dailySessionID keeps WirePod's conversation day aligned with the Hermes
// profiles' daily reset at 04:00 local time. Conversations before that
// boundary remain part of the preceding evening's identity-preserving turn.
func dailySessionID(esn string, now time.Time) string {
	// time.Now carries the process-local zone. Keeping the supplied wall clock
	// also makes the reset boundary unambiguous for callers and tests.
	local := now
	if local.Hour() < 4 {
		local = local.AddDate(0, 0, -1)
	}
	return "wirepod-" + esn + "-" + local.Format("2006-01-02")
}

// HermesConversationEnabled reports configuration only. Keep this separate
// from HermesConversation so a routing decision never creates an agent turn.
func HermesConversationEnabled(esn string) bool {
	targets, err := conversationTargetsFromEnv(os.Getenv(hermesConversationEnv))
	if err != nil {
		return false
	}
	_, ok := targets[strings.ToLower(strings.TrimSpace(esn))]
	return ok
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return strings.TrimSpace(string(runes[:limit])) + "…"
}
