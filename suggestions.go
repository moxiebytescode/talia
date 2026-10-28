package talia

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	defaultOpenAIBase      = "https://api.openai.com/v1"
	systemPrompt           = "You generate domain name ideas. All domain names must end with .com. Do not return any domain without .com."
	userPromptTemplate     = "%s Return %d unique domain suggestions in the 'unverified' array. Each domain must end with .com. Do not return any domain without .com."
	userPromptWithExcludes = "%s Return %d unique domain suggestions in the 'unverified' array. Each domain must end with .com. Do not return any domain without .com. Do NOT suggest any of these existing domains: %s"
	defaultOpenAIModel     = "gpt-5-mini"
	functionName           = "suggest_domains"
	functionDesc           = "Generate domain name ideas."
)

// suggestionSchema defines the JSON structure returned by OpenAI when
// generating domain suggestions. It matches the ExtendedGroupedData
// format used by Talia so the suggestions can be fed back into the
// existing checking workflow.
type suggestionSchema struct {
	Unverified []DomainRecord `json:"unverified"`
}

// httpDoer abstracts HTTP client for testing.
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// GenerateDomainSuggestions requests suggestions using forced tool calling.
// Each request, including its response body, has a two-minute time limit.
func GenerateDomainSuggestions(apiKey, prompt string, count int, model, baseURL string, existingDomains []string) ([]DomainRecord, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	return generateSuggestions(apiKey, prompt, count, model, client, baseURL, existingDomains)
}

// generateSuggestions is the internal implementation that accepts dependencies
// as parameters, enabling parallel tests without shared mutable state.
func generateSuggestions(apiKey, prompt string, count int, model string, client httpDoer, baseURL string, existingDomains []string) ([]DomainRecord, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY is not set")
	}

	ctx := context.Background()

	// Define the tool schema for OpenAI tool calling (modern API format)
	tools := []map[string]any{
		{
			"type": "function",
			"function": map[string]any{
				"name":        functionName,
				"description": functionDesc,
				"parameters": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"unverified": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"domain": map[string]any{"type": "string"},
								},
								"required": []string{"domain"},
							},
						},
					},
					"required":             []string{"unverified"},
					"additionalProperties": false,
				},
			},
		},
	}

	// Build user prompt, including existing domains to avoid if any
	var userContent string
	if len(existingDomains) > 0 {
		userContent = fmt.Sprintf(userPromptWithExcludes, prompt, count, strings.Join(existingDomains, ", "))
	} else {
		userContent = fmt.Sprintf(userPromptTemplate, prompt, count)
	}

	body := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userContent},
		},
		"tools": tools,
		"tool_choice": map[string]any{
			"type":     "function",
			"function": map[string]string{"name": functionName},
		},
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(baseURL, "/")+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openai request: %w", err)
	}
	defer func() {
		_ = resp.Body.Close()
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai status %s", resp.Status)
	}

	var openaiResp struct {
		Choices []struct {
			Message struct {
				ToolCalls []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&openaiResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(openaiResp.Choices) == 0 {
		return nil, fmt.Errorf("no choices returned")
	}
	if len(openaiResp.Choices[0].Message.ToolCalls) == 0 {
		return nil, fmt.Errorf("no tool calls returned")
	}

	function := openaiResp.Choices[0].Message.ToolCalls[0].Function
	if function.Name != functionName {
		return nil, fmt.Errorf("unexpected tool %q: want %s", function.Name, functionName)
	}
	var out suggestionSchema
	if err := json.Unmarshal([]byte(function.Arguments), &out); err != nil {
		return nil, fmt.Errorf("unmarshal structured output: %w", err)
	}
	return out.Unverified, nil
}

// generateSuggestionBatch starts every request with the same exclusion list.
// One receiver owns progress and errors; results retain request order even when
// requests complete out of order. Successful requests survive partial failures.
func generateSuggestionBatch(apiKey, prompt string, count int, model, baseURL string, existingDomains []string, parallel int) ([]DomainRecord, error) {
	type response struct {
		index int
		list  []DomainRecord
		err   error
	}
	responses := make(chan response, parallel)
	for i := range parallel {
		go func() {
			list, err := GenerateDomainSuggestions(apiKey, prompt, count, model, baseURL, existingDomains)
			responses <- response{index: i, list: list, err: err}
		}()
	}
	lists := make([][]DomainRecord, parallel)
	var firstErr error
	for completed := range parallel {
		r := <-responses
		if r.err != nil {
			fmt.Printf("  [%d/%d] Request %d failed: %v\n", completed+1, parallel, r.index+1, r.err)
			if firstErr == nil {
				firstErr = r.err
			}
		} else {
			fmt.Printf("  [%d/%d] Request %d returned %d suggestions\n", completed+1, parallel, r.index+1, len(r.list))
			lists[r.index] = r.list
		}
	}
	var all []DomainRecord
	for _, list := range lists {
		all = append(all, list...)
	}
	return all, firstErr
}
