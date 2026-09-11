package bridge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const hermesConversationEnv = "WIREPOD_HERMES_CONVERSATIONS"

const hermesVoiceSystemPrompt = `You are replying through one Vector's speaker. Be concise, warm, and truthful. Preserve its separate identity and consent-sensitive memory rules.

You control only this Vector through its profile-scoped vector_* tools. Treat a direct request to change your body or surroundings (for example: come here, move closer, look, explore, stop, or say something) as an instruction to act, not an invitation to describe a hypothetical plan. Complete an action chain before answering:
1. Call vector_observe to obtain live state.
2. Before wheel motion, call vector_capture_image when available and only proceed if the immediate route appears safe; never claim it provides precise navigation or person tracking.
3. Use the relevant bounded Vector tool. For “come here” or “move closer,” make only a short, conservative forward vector_drive step toward the current facing direction, then re-observe. If Vector is safely eligible to leave its charger, vector_undock may be part of that chain. Never imply that you can autonomously find a named location or person.
4. Only report physical results returned by successful tools. If an observation, image, or command fails, say so plainly and do not substitute a claimed action.

Be audibly present while doing multi-step embodied work. If you expect to use any Vector tool, first call vector_say with one short acknowledgement (for example, “I hear you — I’m checking.”), then perform the work. Do this before observation, camera, movement, expression, or tool discovery for a physical request. After a verified milestone or a blocker, you may use one more short vector_say update (for example, “I found something,” or “My camera is unavailable.”). Speak only verified, human-useful progress: never narrate hidden reasoning, raw tool calls, guesses, or a stream of filler. Do not speak more than twice before the final answer unless a human asks for ongoing narration.

For an explicit request to say, tell, or announce specific words, call vector_say with the requested short phrase. WirePod speaks your final text automatically, so do not use vector_say merely to duplicate an ordinary final response. After tools complete, give a brief truthful spoken summary of what happened.`

type conversationTarget struct {
	URL      string `json:"url"`
	Key      string `json:"key"`
	Model    string `json:"model"`
	TimeZone string `json:"timezone,omitempty"`
}

type conversationTargets map[string]conversationTarget

type conversationMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type conversationReasoning struct {
	Enabled bool   `json:"enabled"`
	Effort  string `json:"effort,omitempty"`
}

// ErrHermesConversationSuperseded means a newer spoken request from the same
// Vector replaced this turn. It is deliberately distinct from a model or
// network failure: callers must not speak an error for an obsolete request.
var ErrHermesConversationSuperseded = errors.New("Hermes conversation superseded by a newer Vector request")

type hermesConversationTurn struct {
	cancel     context.CancelFunc
	superseded bool
}

// Only one live voice turn per Vector is allowed. Hermes's OpenAI-compatible
// API creates an independent agent for every request, so WirePod owns the
// barge-in policy at the physical voice boundary. Cancelling the prior HTTP
// stream tells Hermes to interrupt that agent before the new turn starts.
var hermesConversationTurns = struct {
	sync.Mutex
	active map[string]*hermesConversationTurn
}{active: make(map[string]*hermesConversationTurn)}

func beginHermesConversationTurn(parent context.Context, esn string) (context.Context, *hermesConversationTurn) {
	hermesConversationTurns.Lock()
	defer hermesConversationTurns.Unlock()
	if prior := hermesConversationTurns.active[esn]; prior != nil {
		prior.superseded = true
		prior.cancel()
	}
	ctx, cancel := context.WithCancel(parent)
	turn := &hermesConversationTurn{cancel: cancel}
	hermesConversationTurns.active[esn] = turn
	return ctx, turn
}

func endHermesConversationTurn(esn string, turn *hermesConversationTurn) {
	hermesConversationTurns.Lock()
	if hermesConversationTurns.active[esn] == turn {
		delete(hermesConversationTurns.active, esn)
	}
	hermesConversationTurns.Unlock()
	turn.cancel()
}

func (turn *hermesConversationTurn) wasSuperseded() bool {
	hermesConversationTurns.Lock()
	defer hermesConversationTurns.Unlock()
	return turn.superseded
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
		target.TimeZone = strings.TrimSpace(target.TimeZone)
		if target.TimeZone != "" {
			if _, err := time.LoadLocation(target.TimeZone); err != nil {
				return nil, os.ErrInvalid
			}
		}
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
	cleanESN := strings.ToLower(strings.TrimSpace(esn))
	requestCtx, turn := beginHermesConversationTurn(ctx, cleanESN)
	defer endHermesConversationTurn(cleanESN, turn)
	conversation := conversationRequest{
		Model:    target.Model,
		Stream:   true,
		Messages: []conversationMessage{{Role: "system", Content: hermesVoiceSystemPrompt}, {Role: "user", Content: transcript}},
	}
	// Qwen's native thinking mode consumes the first part of a short completion
	// before it emits anything speakable. Disable it for the real-time Vector
	// voice path; background/event turns retain each profile's normal reasoning
	// policy and can take the time they need.
	conversation.ModelOptions.Reasoning = conversationReasoning{Enabled: false}
	payload, err := json.Marshal(conversation)
	if err != nil {
		return "", true, false, err
	}
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, target.URL+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", true, false, err
	}
	request.Header.Set("Authorization", "Bearer "+target.Key)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "text/event-stream, application/json")
	request.Header.Set("X-Hermes-Session-Id", dailySessionIDInZone(cleanESN, time.Now(), target.TimeZone))
	request.Header.Set("X-Hermes-Session-Key", "wirepod:vector:"+cleanESN)
	client := &http.Client{Timeout: 45 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	response, err := client.Do(request)
	if err != nil {
		if turn.wasSuperseded() {
			return "", true, false, ErrHermesConversationSuperseded
		}
		return "", true, false, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", true, false, fmt.Errorf("Hermes conversation returned HTTP %d", response.StatusCode)
	}
	if strings.HasPrefix(strings.ToLower(response.Header.Get("Content-Type")), "text/event-stream") {
		answer, err := readHermesSSE(response.Body, onChunk)
		if err != nil {
			if turn.wasSuperseded() {
				return "", true, true, ErrHermesConversationSuperseded
			}
			return "", true, true, err
		}
		if turn.wasSuperseded() {
			return "", true, true, ErrHermesConversationSuperseded
		}
		return truncateRunes(answer, 480), true, true, nil
	}
	answer, err = readHermesJSON(response.Body)
	if err != nil {
		if turn.wasSuperseded() {
			return "", true, false, ErrHermesConversationSuperseded
		}
		return "", true, false, err
	}
	if turn.wasSuperseded() {
		return "", true, false, ErrHermesConversationSuperseded
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
		return "", fmt.Errorf("Hermes stream read failed: %w", err)
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
	return dailySessionIDInZone(esn, now, "")
}

func dailySessionIDInZone(esn string, now time.Time, zone string) string {
	if zone != "" {
		if location, err := time.LoadLocation(zone); err == nil {
			now = now.In(location)
		}
	}
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
