package processreqs

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"github.com/kercre123/wire-pod/chipper/pkg/vtt"
	"github.com/kercre123/wire-pod/chipper/pkg/wirepod/bridge"
	"github.com/kercre123/wire-pod/chipper/pkg/wirepod/sdkapp"
	sr "github.com/kercre123/wire-pod/chipper/pkg/wirepod/speechrequest"
	ttr "github.com/kercre123/wire-pod/chipper/pkg/wirepod/ttr"
)

const hermesIntentFallbackSpeechMaxDelay = 15 * time.Second

// This is here for compatibility with 1.6 and older software
func (s *Server) ProcessIntent(req *vtt.IntentRequest) (*vtt.IntentResponse, error) {
	var successMatched bool
	speechReq := sr.ReqToSpeechRequest(req)
	var transcribedText string
	if !isSti {
		var err error
		transcribedText, err = sttHandler(speechReq)
		if err != nil {
			ttr.IntentPass(req, "intent_system_noaudio", "voice processing error: "+err.Error(), map[string]string{"error": err.Error()}, true)
			return nil, nil
		}
		if strings.TrimSpace(transcribedText) == "" {
			ttr.IntentPass(req, "intent_system_noaudio", "", map[string]string{}, false)
			return nil, nil
		}
		if replyFromHermesIntent(speechReq.Device, transcribedText) {
			ttr.IntentPass(req, "intent_knowledge_promptquestion", transcribedText, map[string]string{}, false)
			return nil, nil
		}
		successMatched = ttr.ProcessTextAll(req, transcribedText, vars.IntentList, speechReq.IsOpus)
	} else {
		intent, slots, err := stiHandler(speechReq)
		if err != nil {
			if err.Error() == "inference not understood" {
				logger.Println("No intent was matched")
				ttr.IntentPass(req, "intent_system_unmatched", "voice processing error", map[string]string{"error": err.Error()}, true)
				return nil, nil
			}
			logger.Println(err)
			ttr.IntentPass(req, "intent_system_noaudio", "voice processing error", map[string]string{"error": err.Error()}, true)
			return nil, nil
		}
		ttr.ParamCheckerSlotsEnUS(req, intent, slots, speechReq.IsOpus, speechReq.Device)
		return nil, nil
	}
	if !successMatched {
		if vars.APIConfig.Knowledge.IntentGraph && vars.APIConfig.Knowledge.Enable {
			logger.Println("Making LLM request for device " + req.Device + "...")
			_, err := ttr.StreamingKGSim(req, req.Device, transcribedText, false)
			if err != nil {
				logger.Println("LLM error: " + err.Error())
				logger.LogUI("LLM error: " + err.Error())
				ttr.IntentPass(req, "intent_system_unmatched", transcribedText, map[string]string{"": ""}, false)
				ttr.KGSim(req.Device, "There was an error getting a response from the L L M. Check the logs in the web interface.")
			}
			logger.Println("Bot " + speechReq.Device + " request served.")
			return nil, nil
		}
		logger.Println("No intent was matched.")
		ttr.IntentPass(req, "intent_system_unmatched", transcribedText, map[string]string{"": ""}, false)
		return nil, nil
	}
	logger.Println("Bot " + speechReq.Device + " request served.")
	return nil, nil
}

func replyFromHermesIntent(serial, text string) bool {
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	spokeChunk := false
	answer, configured, streamed, err := bridge.HermesConversationStream(ctx, serial, text, func(chunk string) error {
		if err := speakHermesIntentReply(serial, chunk); err != nil {
			logger.Println("Hermes intent chunk speech failed: " + err.Error())
			return nil
		}
		spokeChunk = true
		return nil
	})
	if !configured {
		return false
	}
	if err != nil {
		if errors.Is(err, bridge.ErrHermesConversationSuperseded) {
			logger.Println("Hermes intent turn superseded by newer Vector speech")
			return true
		}
		logger.Println("Hermes intent request failed: " + err.Error())
		if !spokeChunk && shouldSpeakHermesIntentFallback(started, time.Now()) {
			if speakErr := speakHermesIntentReply(serial, "I’m sorry, I’m having trouble thinking right now. Please try again shortly."); speakErr != nil {
				logger.Println("Hermes intent fallback speech failed: " + speakErr.Error())
			}
		} else if !spokeChunk {
			logger.Println("Suppressing stale Hermes intent fallback speech")
		}
		return true
	}
	// A non-streaming Hermes response has not been spoken by the callback.
	// A stream which only carried non-content metadata likewise falls back to
	// the completed answer instead of leaving Vector silent.
	if !streamed || !spokeChunk {
		if speakErr := speakHermesIntentReply(serial, answer); speakErr != nil {
			logger.Println("Hermes intent speech failed: " + speakErr.Error())
		}
	}
	return true
}

// shouldSpeakHermesIntentFallback prevents an abandoned or delayed speech
// request from startling someone with an error long after they stopped talking.
// Healthy streaming turns emit their first speakable chunk well inside this
// window; an immediate failure still receives useful audible feedback.
func shouldSpeakHermesIntentFallback(started, now time.Time) bool {
	return !started.IsZero() && !now.Before(started) && now.Sub(started) <= hermesIntentFallbackSpeechMaxDelay
}

func speakHermesIntentReply(serial, text string) error {
	text = boundedHermesSpeech(text)
	if text == "" {
		return nil
	}
	if _, err := sdkapp.HermesControl(serial, sdkapp.HermesCommand{Action: "say", Text: text}); err != nil {
		return err
	}
	return nil
}

func boundedHermesSpeech(text string) string {
	text = strings.TrimSpace(text)
	if len([]rune(text)) > 280 {
		return string([]rune(text)[:277]) + "..."
	}
	return text
}
