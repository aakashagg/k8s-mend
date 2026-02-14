package main

import (
	"encoding/json"
	"net/http"

	"ai-service.com/flows"
)

func main() {
	http.HandleFunc("/evaluate", func(w http.ResponseWriter, r *http.Request) {
		var req flows.EvaluationRequest

		json.NewDecoder(r.Body).Decode(&req)

		prompt := flows.BuildPrompt(req)
		resp, err := flows.CallLLM(prompt)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}

		json.NewEncoder(w).Encode(resp)
	})

	http.ListenAndServe(":8081", nil)
}
