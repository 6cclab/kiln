package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/provider"
)

// responsesTextSignature stores wire identity beside the text it describes.
// The digest prevents replaying an ID or phase after a context transform edits
// the text. Source fields prevent replay across API shapes, providers or models.
type responsesTextSignature struct {
	Version  int    `json:"v"`
	API      string `json:"api"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	ID       string `json:"id"`
	Phase    string `json:"phase,omitempty"`
	Digest   string `json:"sha256"`
}

func responsesTextDigest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

func validResponsesPhase(phase string) bool {
	return phase == "" || phase == "commentary" || phase == "partial_answer" || phase == "final_answer"
}

func stampResponsesText(c msg.TextContent, source msg.AssistantMessage, id, phase string) msg.TextContent {
	c.TextSignature = ""
	if id == "" || !validResponsesPhase(phase) {
		return c
	}
	sig := responsesTextSignature{
		Version: 1, API: source.API, Provider: source.Provider, Model: source.Model,
		ID: id, Phase: phase, Digest: responsesTextDigest(c.Text),
	}
	b, err := json.Marshal(sig)
	if err == nil {
		c.TextSignature = string(b)
	}
	return c
}

func parseResponsesTextSignature(c msg.TextContent, source msg.AssistantMessage, model provider.Model, api provider.Api) (responsesTextSignature, bool) {
	var sig responsesTextSignature
	if source.API != string(api) || source.Provider != model.Provider || source.Model != model.ID {
		return sig, false
	}
	if json.Unmarshal([]byte(c.TextSignature), &sig) != nil || sig.Version != 1 ||
		sig.API != source.API || sig.Provider != source.Provider || sig.Model != source.Model ||
		sig.ID == "" || sig.Digest != responsesTextDigest(c.Text) || !validResponsesPhase(sig.Phase) {
		return responsesTextSignature{}, false
	}
	return sig, true
}

func replayResponsesText(c msg.TextContent, source msg.AssistantMessage, model provider.Model, api provider.Api, msgIndex, textBlockIndex int) (string, string) {
	if sig, ok := parseResponsesTextSignature(c, source, model, api); ok {
		return sig.ID, sig.Phase
	}
	id := "msg_pi_" + strconv.Itoa(msgIndex)
	if textBlockIndex > 0 {
		id += "_" + strconv.Itoa(textBlockIndex)
	}
	return id, ""
}
