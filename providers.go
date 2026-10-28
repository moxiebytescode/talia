package talia

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// suggestionConfig describes a resolved provider selection. Credentials stay in
// memory and are sent only through the request's authorization header.
type suggestionConfig struct {
	provider    string
	apiKey      string
	model       string
	baseURL     string
	workspaceID string
}

// suggestionJSONSchema requires an unverified array of closed objects with domain strings.
func suggestionJSONSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"unverified": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"domain": map[string]any{"type": "string"},
					},
					"required":             []string{"domain"},
					"additionalProperties": false,
				},
			},
		},
		"required":             []string{"unverified"},
		"additionalProperties": false,
	}
}

// generateProviderSuggestions routes a resolved provider request and returns
// normalized, unique suggestions after applying the exclusion list.
func generateProviderSuggestions(config suggestionConfig, prompt string, count int, existingDomains []string, client httpDoer) ([]DomainRecord, error) {
	if config.provider != "openai" && config.provider != "anthropic" && config.provider != "openai-compatible" {
		return nil, fmt.Errorf("unsupported suggestion provider")
	}
	if config.apiKey == "" {
		return nil, fmt.Errorf("%s API key is not set", config.provider)
	}
	if config.model == "" {
		return nil, fmt.Errorf("suggestion model is not set")
	}
	if count < 1 {
		return nil, fmt.Errorf("suggestion count must be positive")
	}
	userContent := suggestionPrompt(prompt, count, existingDomains)
	var content string
	var err error
	switch config.provider {
	case "openai":
		content, err = generateOpenAIResponse(config, userContent, client)
	case "anthropic":
		content, err = generateAnthropicMessage(config, userContent, client)
	case "openai-compatible":
		content, err = generateChatCompletion(config, userContent, client)
	}
	if err != nil {
		return nil, err
	}
	return decodeSuggestions(content, existingDomains)
}

// requestSuggestions deliberately omits provider bodies and transport error text
// from errors: endpoints may include credentials or echo request content.
func requestSuggestions(config suggestionConfig, path string, body any, client httpDoer, result any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal suggestion request")
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, strings.TrimRight(config.baseURL, "/")+path, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("invalid suggestion endpoint")
	}
	req.Header.Set("Authorization", "Bearer "+config.apiKey)
	req.Header.Set("Content-Type", "application/json")
	if config.provider == "anthropic" {
		req.Header.Set("anthropic-version", anthropicVersion)
		if config.workspaceID != "" {
			req.Header.Set("anthropic-workspace-id", config.workspaceID)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%s request failed", requestProviderName(config.provider))
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s status %d", requestProviderName(config.provider), resp.StatusCode)
	}
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(result); err != nil {
		return fmt.Errorf("decode response: invalid provider response")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("decode response: unexpected trailing content")
	}
	return nil
}

// requestProviderName retains the legacy OpenAI label for compatibility errors.
func requestProviderName(provider string) string {
	if provider == "openai-compatible" {
		return "openai"
	}
	return provider
}

// hasProviderError detects a present, non-null error field.
func hasProviderError(raw json.RawMessage) bool {
	return len(raw) > 0 && !bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// generateOpenAIResponse requests strict structured output through Responses and
// extracts message text from a completed response, rejecting refusals.
func generateOpenAIResponse(config suggestionConfig, userContent string, client httpDoer) (string, error) {
	body := map[string]any{
		"model":        config.model,
		"instructions": systemPrompt,
		"input":        userContent,
		"store":        false,
		"text": map[string]any{
			"format": map[string]any{
				"type":   "json_schema",
				"name":   functionName,
				"strict": true,
				"schema": suggestionJSONSchema(),
			},
		},
	}
	var response struct {
		Status string          `json:"status"`
		Error  json.RawMessage `json:"error"`
		Output []struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := requestSuggestions(config, "/responses", body, client, &response); err != nil {
		return "", err
	}
	if hasProviderError(response.Error) {
		return "", fmt.Errorf("openai returned a response error")
	}
	if response.Status == "incomplete" {
		return "", fmt.Errorf("openai response incomplete")
	}
	if response.Status != "completed" {
		return "", fmt.Errorf("openai response did not complete")
	}
	var text strings.Builder
	for _, output := range response.Output {
		if output.Type != "message" {
			continue
		}
		if output.Status != "" && output.Status != "completed" {
			return "", fmt.Errorf("openai output message did not complete")
		}
		for _, content := range output.Content {
			if content.Type == "refusal" {
				return "", fmt.Errorf("openai refused the suggestion request")
			}
			if content.Type == "output_text" {
				text.WriteString(content.Text)
			}
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		return "", fmt.Errorf("openai returned no suggestion text")
	}
	return text.String(), nil
}

// generateAnthropicMessage requests structured output through Messages and
// extracts text from a completed turn, skipping thinking content.
func generateAnthropicMessage(config suggestionConfig, userContent string, client httpDoer) (string, error) {
	body := map[string]any{
		"model":      config.model,
		"max_tokens": 8192,
		"system":     systemPrompt,
		"messages":   []map[string]string{{"role": "user", "content": userContent}},
		"output_config": map[string]any{
			"format": map[string]any{
				"type":   "json_schema",
				"schema": suggestionJSONSchema(),
			},
		},
	}
	var response struct {
		Type       string          `json:"type"`
		StopReason string          `json:"stop_reason"`
		Error      json.RawMessage `json:"error"`
		Content    []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := requestSuggestions(config, "/messages", body, client, &response); err != nil {
		return "", err
	}
	if response.Type == "error" || hasProviderError(response.Error) {
		return "", fmt.Errorf("anthropic returned a response error")
	}
	if response.StopReason == "refusal" {
		return "", fmt.Errorf("anthropic refused the suggestion request")
	}
	if response.StopReason == "max_tokens" {
		return "", fmt.Errorf("anthropic response incomplete: max_tokens reached")
	}
	if response.Type != "message" || response.StopReason != "end_turn" {
		return "", fmt.Errorf("anthropic response did not complete")
	}
	var text strings.Builder
	for _, content := range response.Content {
		switch content.Type {
		case "thinking", "redacted_thinking":
			continue
		case "text":
			text.WriteString(content.Text)
		default:
			return "", fmt.Errorf("anthropic returned unexpected suggestion content")
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		return "", fmt.Errorf("anthropic returned no suggestion text")
	}
	return text.String(), nil
}

// generateChatCompletion requests one forced suggestion tool call and returns its
// arguments, rejecting refusals and explicitly unfinished responses.
func generateChatCompletion(config suggestionConfig, userContent string, client httpDoer) (string, error) {
	body := map[string]any{
		"model": config.model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userContent},
		},
		"tools": []map[string]any{{
			"type": "function",
			"function": map[string]any{
				"name": functionName, "description": functionDesc, "parameters": suggestionJSONSchema(),
			},
		}},
		"tool_choice": map[string]any{
			"type": "function", "function": map[string]string{"name": functionName},
		},
	}
	// Luna's Chat Completions API requires reasoning to be disabled for tools.
	if config.model == "gpt-6-luna" {
		body["reasoning_effort"] = "none"
	}
	var response struct {
		Error   json.RawMessage `json:"error"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Refusal   *string `json:"refusal"`
				ToolCalls []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := requestSuggestions(config, "/chat/completions", body, client, &response); err != nil {
		return "", err
	}
	if hasProviderError(response.Error) {
		return "", fmt.Errorf("openai-compatible endpoint returned a response error")
	}
	if len(response.Choices) == 0 {
		return "", fmt.Errorf("no choices returned")
	}
	choice := response.Choices[0]
	if choice.Message.Refusal != nil {
		return "", fmt.Errorf("openai-compatible endpoint refused the suggestion request")
	}
	if choice.FinishReason != "" && choice.FinishReason != "tool_calls" {
		return "", fmt.Errorf("openai-compatible tool response did not complete")
	}
	if len(choice.Message.ToolCalls) == 0 {
		return "", fmt.Errorf("no tool calls returned")
	}
	if len(choice.Message.ToolCalls) != 1 {
		return "", fmt.Errorf("expected exactly one suggestion tool call")
	}
	function := choice.Message.ToolCalls[0].Function
	if function.Name != functionName {
		return "", fmt.Errorf("unexpected suggestion tool")
	}
	return function.Arguments, nil
}

// decodeSuggestions verifies the complete JSON shape before accepting any names.
// Schema conformance does not replace domain normalization or exclusion filtering.
func decodeSuggestions(content string, existingDomains []string) ([]DomainRecord, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &root); err != nil || len(root) != 1 {
		return nil, fmt.Errorf("unmarshal structured output: expected an unverified array")
	}
	var records []map[string]json.RawMessage
	if err := json.Unmarshal(root["unverified"], &records); err != nil || records == nil {
		return nil, fmt.Errorf("unmarshal structured output: expected an unverified array")
	}
	var names []string
	for _, record := range records {
		var domain *string
		if len(record) != 1 {
			return nil, fmt.Errorf("unmarshal structured output: expected domain strings")
		}
		if err := json.Unmarshal(record["domain"], &domain); err != nil || domain == nil {
			return nil, fmt.Errorf("unmarshal structured output: expected domain strings")
		}
		names = append(names, *domain)
	}
	var set domainSet
	for _, existing := range existingDomains {
		set.include(existing)
	}
	var out []DomainRecord
	for _, name := range names {
		if domain := set.include(name); domain != "" {
			out = append(out, DomainRecord{Domain: domain})
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("provider returned no usable suggestions")
	}
	return out, nil
}
