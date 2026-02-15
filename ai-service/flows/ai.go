package flows

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/google/generative-ai-go/genai"
	"google.golang.org/api/option"
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
	ctx := context.Background()
	client, err := genai.NewClient(ctx, option.WithAPIKey(os.Getenv("GEMINI_API_KEY")))
	if err != nil {
		return EvaluationResponse{}, err
	}
	defer client.Close()

	model := client.GenerativeModel("gemini-1.5-flash")
	model.ResponseMIMEType = "application/json"
	model.SystemInstruction = &genai.Content{
		Parts: []genai.Part{genai.Text("You are a Kubernetes reliability assistant.")},
	}

	resp, err := model.GenerateContent(ctx, genai.Text(prompt))
	if err != nil {
		return EvaluationResponse{}, err
	}

	if len(resp.Candidates) == 0 || resp.Candidates[0].Content == nil {
		return EvaluationResponse{}, fmt.Errorf("no response from Gemini")
	}

	var responseText string
	for _, part := range resp.Candidates[0].Content.Parts {
		if txt, ok := part.(genai.Text); ok {
			responseText += string(txt)
		}
	}

	var result EvaluationResponse
	err = json.Unmarshal([]byte(responseText), &result)
	return result, err
}
