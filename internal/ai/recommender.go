package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/toolhub-dev/toolhub/internal/security"
	"github.com/toolhub-dev/toolhub/internal/store"
)

type Recommender struct {
	store *store.Store
	http  *http.Client
}

type Request struct {
	Requirement string         `json:"requirement"`
	Inventory   map[string]any `json:"inventory"`
	Tags        []string       `json:"tags"`
}

type Candidate struct {
	Name       string   `json:"name"`
	SourceHint string   `json:"sourceHint"`
	Reasons    []string `json:"reasons"`
	Risks      []string `json:"risks"`
	Confidence float64  `json:"confidence"`
}

func New(st *store.Store) *Recommender {
	return &Recommender{store: st, http: &http.Client{Timeout: 30 * time.Second}}
}

func (r *Recommender) Recommend(ctx context.Context, input Request) ([]Candidate, error) {
	input.Requirement = strings.TrimSpace(input.Requirement)
	if len(input.Requirement) < 5 || len(input.Requirement) > 2000 {
		return nil, errors.New("requirement must contain 5-2000 characters")
	}
	provider, err := r.store.DefaultAIProvider(ctx)
	if err != nil {
		return nil, err
	}
	sanitized := map[string]any{"requirement": input.Requirement, "inventory": security.RedactMap(input.Inventory), "tags": input.Tags}
	contextJSON, _ := json.Marshal(sanitized)
	payload := map[string]any{
		"model":           provider.Model,
		"temperature":     0.2,
		"response_format": map[string]string{"type": "json_object"},
		"messages": []map[string]string{
			{"role": "system", "content": "You recommend Skills packages for Codex, Claude, and Hermes. Return JSON with a candidates array. Each candidate has name, sourceHint, reasons, risks, and confidence. Never claim to install anything."},
			{"role": "user", "content": string(contextJSON)},
		},
	}
	encoded, _ := json.Marshal(payload)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(provider.BaseURL, "/")+"/chat/completions", bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+provider.APIKey)
	request.Header.Set("Content-Type", "application/json")
	response, err := r.http.Do(request)
	if err != nil {
		return nil, fmt.Errorf("AI provider request: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("AI provider returned HTTP %d", response.StatusCode)
	}
	var completion struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &completion); err != nil || len(completion.Choices) == 0 {
		return nil, errors.New("AI provider returned an invalid completion")
	}
	var result struct {
		Candidates []Candidate `json:"candidates"`
	}
	if err := json.Unmarshal([]byte(completion.Choices[0].Message.Content), &result); err != nil {
		return nil, errors.New("AI provider returned invalid recommendation JSON")
	}
	if len(result.Candidates) > 20 {
		result.Candidates = result.Candidates[:20]
	}
	return result.Candidates, nil
}
