package vars

import "testing"

func TestWriteSTTPersistsLanguageForRemoteWhisper(t *testing.T) {
	previous := APIConfig
	t.Cleanup(func() { APIConfig = previous })
	t.Setenv("STT_SERVICE", "whisper")
	t.Setenv("STT_LANGUAGE", "en-US")
	WriteSTT()
	if APIConfig.STT.Service != "whisper" || APIConfig.STT.Language != "en-US" {
		t.Fatalf("remote Whisper config was not persisted: %+v", APIConfig.STT)
	}
}
