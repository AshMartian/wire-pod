package processreqs

import (
	"context"
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
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	answer, configured, err := bridge.HermesConversation(ctx, serial, text)
	if !configured {
		return false
	}
	if err != nil {
		logger.Println("Hermes intent request failed: " + err.Error())
		speakHermesIntentReply(serial, "I’m sorry, I’m having trouble thinking right now. Please try again shortly.")
		return true
	}
	speakHermesIntentReply(serial, answer)
	return true
}

func speakHermesIntentReply(serial, text string) {
	text = boundedHermesSpeech(text)
	if text == "" {
		return
	}
	if _, err := sdkapp.HermesControl(serial, sdkapp.HermesCommand{Action: "say", Text: text}); err != nil {
		logger.Println("Hermes intent speech failed: " + err.Error())
	}
}

func boundedHermesSpeech(text string) string {
	text = strings.TrimSpace(text)
	if len([]rune(text)) > 280 {
		return string([]rune(text)[:277]) + "..."
	}
	return text
}
