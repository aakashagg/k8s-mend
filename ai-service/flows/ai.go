package flows

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/firebase/genkit/go/plugins/compat_oai/openai"
)

type EvaluationRequest struct {
	Policy struct {
		AllowedActions []string `json:"allowedActions"`
		Mode           string   `json:"mode"`
	} `json:"policy"`

	Resource struct {
		Kind         string `json:"kind"`
		Name         string `json:"name"`
		Namespace    string `json:"namespace"`
		RestartCount int32  `json:"restartCount"`
		Reason       string `json:"reason"`
		AgeSeconds   int64  `json:"ageSeconds"`
	} `json:"resource"`
}

type EvaluationResponse struct {
	Action     string  `json:"action"`
	Confidence float64 `json:"confidence"`
	Reason     string  `json:"reason"`
}

func BuildPrompt(req EvaluationRequest) string {
	return fmt.Sprintf(`
You are a Kubernetes reliability AI.

Allowed actions:
%s

Resource:
Kind: %s
Name: %s
Namespace: %s
RestartCount: %d
Reason: %s
AgeSeconds: %d

Rules:
- Only choose from allowed actions.
- If unsure, return "NoAction".
- Return JSON only in this format:
{
  "action": string,
  "confidence": number between 0 and 1,
  "reason": string
}
`,
		strings.Join(req.Policy.AllowedActions, ", "),
		req.Resource.Kind,
		req.Resource.Name,
		req.Resource.Namespace,
		req.Resource.RestartCount,
		req.Resource.Reason,
		req.Resource.AgeSeconds,
	)
}

func CallLLM(prompt string) (EvaluationResponse, error) {
	client := openai.NewClient(os.Getenv("OPENAI_API_KEY"))

	resp, err := client.Chat.Completions.New(context.Background(), openai.ChatCompletionNewParams{
		Model: "gpt-4o-mini",
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.SystemMessage("You are a Kubernetes reliability assistant."),
			openai.UserMessage(prompt),
		},
	})
	if err != nil {
		return EvaluationResponse{}, err
	}

	var result EvaluationResponse
	err = json.Unmarshal([]byte(resp.Choices[0].Message.Content), &result)
	return result, err
}
