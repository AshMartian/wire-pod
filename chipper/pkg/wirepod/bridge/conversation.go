package bridge

import (
	"bufio"
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

type conversationMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type conversationReasoning struct {
	Enabled bool   `json:"enabled"`
	Effort  string `json:"effort"`
}

type conversationRequest struct {
	Model        string `json:"model"`
	Stream       bool   `json:"stream"`
	ModelOptions struct {
		Reasoning conversationReasoning `json:"reasoning"`
	} `json:"model_options"`
	Messages []conversationMessage `json:"messages"`
}

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

// HermesConversation sends a transcribed Vector request to its own Hermes API
// profile and waits for the complete response. Call HermesConversationStream
// when speech can begin before the agent has completed its reply.
func HermesConversation(ctx context.Context, esn, transcript string) (string, bool, error) {
	answer, configured, _, err := HermesConversationStream(ctx, esn, transcript, nil)
	return answer, configured, err
}

// HermesConversationStream sends a transcribed Vector request to Hermes and
// emits complete, Vector-safe sentence chunks as OpenAI-compatible SSE deltas
// arrive. Hermes installations which return a normal chat-completion JSON
// response remain supported; callers can use streamed to choose their normal
// non-streaming response path. The session ID intentionally rotates each
// local day while the stable session key keeps consented long-term memory
// scoped to the same physical robot.
func HermesConversationStream(ctx context.Context, esn, transcript string, onChunk func(string) error) (answer string, configured bool, streamed bool, err error) {
	targets, err := conversationTargetsFromEnv(os.Getenv(hermesConversationEnv))
	if err != nil || len(targets) == 0 {
		return "", false, false, nil
	}
	target, ok := targets[strings.ToLower(strings.TrimSpace(esn))]
	if !ok {
		return "", false, false, nil
	}
	transcript = strings.TrimSpace(transcript)
	if transcript == "" || len([]rune(transcript)) > 2000 {
		return "", true, false, errors.New("invalid Vector transcript")
	}
	conversation := conversationRequest{
		Model:    target.Model,
		Stream:   true,
		Messages: []conversationMessage{{Role: "system", Content: "You are replying through one Vector's speaker. Be concise, warm, and truthful. Preserve its separate identity and consent-sensitive memory rules. WirePod will speak your final text, so do not call robot speech or motion tools unless the user explicitly asks for an embodied action."}, {Role: "user", Content: transcript}},
	}
	// Hermes passes this explicit setting to LM Studio. Keeping it in the
	// request prevents a future global profile default from slowing robot turns.
	conversation.ModelOptions.Reasoning = conversationReasoning{Enabled: true, Effort: "low"}
	payload, err := json.Marshal(conversation)
	if err != nil {
		return "", true, false, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, target.URL+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", true, false, err
	}
	cleanESN := strings.ToLower(strings.TrimSpace(esn))
	request.Header.Set("Authorization", "Bearer "+target.Key)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream, application/json")
	request.Header.Set("X-Hermes-Session-Id", dailySessionID(cleanESN, time.Now()))
	request.Header.Set("X-Hermes-Session-Key", "wirepod:vector:"+cleanESN)
	client := &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		return "", true, false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", true, false, errors.New("Hermes conversation request failed")
	}
	if strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		answer, err := readHermesSSE(response.Body, onChunk)
		if err != nil {
			return "", true, true, err
		}
		return truncateRunes(answer, 480), true, true, nil
	}
	answer, err = readHermesJSON(response.Body)
	if err != nil {
		return "", true, false, err
	}
	return truncateRunes(answer, 480), true, false, nil
}

func readHermesJSON(reader io.Reader) (string, error) {
	body, err := io.ReadAll(io.LimitReader(reader, 128*1024+1))
	if err != nil || len(body) > 128*1024 {
		return "", errors.New("invalid Hermes conversation response")
	}
	var result struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &result); err != nil || len(result.Choices) == 0 {
		return "", errors.New("invalid Hermes conversation response")
	}
	answer := strings.TrimSpace(result.Choices[0].Message.Content)
	if answer == "" {
		return "", errors.New("empty Hermes conversation response")
	}
	return answer, nil
}

func readHermesSSE(reader io.Reader, onChunk func(string) error) (string, error) {
	scanner := bufio.NewScanner(io.LimitReader(reader, 128*1024+1))
	scanner.Buffer(make([]byte, 4*1024), 128*1024)
	var answer strings.Builder
	chunker := newHermesSentenceChunker()
	done := false
	emit := func(text string) error {
		if text == "" || onChunk == nil {
			return nil
		}
		return onChunk(text)
	}
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" {
			done = true
			break
		}
		var event struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return "", errors.New("invalid Hermes stream event")
		}
		for _, choice := range event.Choices {
			content := choice.Delta.Content
			if content == "" {
				continue
			}
			if answer.Len()+len(content) > 128*1024 {
				return "", errors.New("invalid Hermes conversation response")
			}
			answer.WriteString(content)
			for _, chunk := range chunker.push(content) {
				if err := emit(chunk); err != nil {
					return "", err
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", errors.New("invalid Hermes conversation response")
	}
	if !done {
		return "", errors.New("incomplete Hermes conversation stream")
	}
	if answer.Len() == 0 {
		return "", errors.New("empty Hermes conversation response")
	}
	for _, chunk := range chunker.finish() {
		if err := emit(chunk); err != nil {
			return "", err
		}
	}
	return answer.String(), nil
}

// hermesSentenceChunker holds back partial phrases so Vector speaks natural
// sentence-sized turns. The hard limit matches HermesControl's public speech
// contract even if a model produces a very long sentence.
type hermesSentenceChunker struct {
	pending []rune
}

func newHermesSentenceChunker() *hermesSentenceChunker {
	return &hermesSentenceChunker{}
}

func (c *hermesSentenceChunker) push(delta string) []string {
	c.pending = append(c.pending, []rune(delta)...)
	return c.drain(false)
}

func (c *hermesSentenceChunker) finish() []string {
	return c.drain(true)
}

func (c *hermesSentenceChunker) drain(final bool) []string {
	var chunks []string
	for len(c.pending) > 0 {
		end := -1
		limit := len(c.pending)
		if limit > 280 {
			limit = 280
		}
		for index := 0; index < limit; index++ {
			if strings.ContainsRune(".!?", c.pending[index]) {
				end = index + 1
			}
		}
		if end == -1 && len(c.pending) > 280 {
			end = 280
		}
		if end == -1 && final {
			end = len(c.pending)
		}
		if end == -1 {
			break
		}
		chunk := strings.TrimSpace(string(c.pending[:end]))
		c.pending = c.pending[end:]
		if chunk != "" {
			chunks = append(chunks, chunk)
		}
	}
	return chunks
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
