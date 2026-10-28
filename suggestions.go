package talia

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	defaultOpenAIBase      = "https://api.openai.com/v1"
	defaultAnthropicBase   = "https://api.anthropic.com/v1"
	defaultOpenAIModel     = "gpt-6.1-terra"
	defaultCompatibleModel = "gpt-6-luna"
	defaultAnthropicModel  = "claude-haiku-5-5"
	anthropicVersion       = "2026-10-21"
	systemPrompt           = "You generate domain name ideas. All domain names must end with .com. Do not return any domain without .com."
	userPromptTemplate     = "%s Return %d unique domain suggestions in the 'unverified' array. Each domain must end with .com. Do not return any domain without .com."
	userPromptWithExcludes = "%s Return %d unique domain suggestions in the 'unverified' array. Each domain must end with .com. Do not return any domain without .com. Do NOT suggest any of these existing domains: %s"
	functionName           = "suggest_domains"
	functionDesc           = "Generate domain name ideas."
)

// httpDoer abstracts HTTP client for testing.
type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

// GenerateDomainSuggestions requests suggestions through OpenAI-compatible Chat
// Completions. Each request, including its response body, has a two-minute limit.
// The CLI selects native provider APIs separately.
func GenerateDomainSuggestions(apiKey, prompt string, count int, model, baseURL string, existingDomains []string) ([]DomainRecord, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	return generateSuggestions(apiKey, prompt, count, model, client, baseURL, existingDomains)
}

// generateSuggestions preserves the Chat Completions interface for library callers.
func generateSuggestions(apiKey, prompt string, count int, model string, client httpDoer, baseURL string, existingDomains []string) ([]DomainRecord, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY is not set")
	}
	config := suggestionConfig{provider: "openai-compatible", apiKey: apiKey, model: model, baseURL: baseURL}
	return generateProviderSuggestions(config, prompt, count, existingDomains, client)
}

// suggestionPrompt combines the user's request, target count, and optional exclusions.
func suggestionPrompt(prompt string, count int, existingDomains []string) string {
	if len(existingDomains) > 0 {
		return fmt.Sprintf(userPromptWithExcludes, prompt, count, strings.Join(existingDomains, ", "))
	}
	return fmt.Sprintf(userPromptTemplate, prompt, count)
}

// generateProviderSuggestionBatch runs concurrent requests through the selected
// provider, giving each request a two-minute HTTP timeout.
func generateProviderSuggestionBatch(config suggestionConfig, prompt string, count int, existingDomains []string, parallel int) ([]DomainRecord, error) {
	client := &http.Client{Timeout: 2 * time.Minute}
	return collectSuggestionBatch(parallel, func() ([]DomainRecord, error) {
		return generateProviderSuggestions(config, prompt, count, existingDomains, client)
	})
}

// collectSuggestionBatch starts every request with the same exclusion list.
// One receiver owns progress and errors; results retain request order even when
// requests complete out of order. Successful requests survive partial failures.
func collectSuggestionBatch(parallel int, request func() ([]DomainRecord, error)) ([]DomainRecord, error) {
	if parallel < 1 {
		parallel = 1
	}
	type response struct {
		index int
		list  []DomainRecord
		err   error
	}
	responses := make(chan response, parallel)
	for i := range parallel {
		go func() {
			list, err := request()
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
