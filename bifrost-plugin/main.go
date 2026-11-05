package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/maximhq/bifrost/core/schemas"
)

const pluginName = "proofpulse-freshness"

type pluginConfig struct {
	ServiceURL string `json:"service_url"`
	TimeoutSec int    `json:"timeout_seconds"`
}

var (
	configMu sync.RWMutex
	config   = pluginConfig{ServiceURL: "http://127.0.0.1:8000", TimeoutSec: 180}
	client   = &http.Client{Timeout: 180 * time.Second}
)

type chatRequest struct {
	Messages []struct {
		Role    string          `json:"role"`
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
	Stream bool `json:"stream"`
}

type verifyRequest struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

type verifyResponse struct {
	VerifiedAnswer string `json:"verified_answer"`
	Verifications  []struct {
		Claim   string `json:"claim"`
		Status  string `json:"status"`
		Sources []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
		} `json:"sources"`
	} `json:"verifications"`
}

// Init is called by Bifrost after loading the shared object.
func Init(raw any) error {
	if raw == nil {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("encode plugin configuration: %w", err)
	}
	var parsed pluginConfig
	if err := json.Unmarshal(encoded, &parsed); err != nil {
		return fmt.Errorf("decode plugin configuration: %w", err)
	}
	configMu.Lock()
	defer configMu.Unlock()
	if strings.TrimSpace(parsed.ServiceURL) != "" {
		config.ServiceURL = strings.TrimRight(strings.TrimSpace(parsed.ServiceURL), "/")
	}
	if parsed.TimeoutSec > 0 {
		config.TimeoutSec = parsed.TimeoutSec
	}
	parsedURL, err := url.Parse(config.ServiceURL)
	if err != nil || parsedURL.Host == "" || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") {
		return errors.New("service_url must be an absolute HTTP or HTTPS URL")
	}
	client = &http.Client{Timeout: time.Duration(config.TimeoutSec) * time.Second}
	return nil
}

// GetName is required by Bifrost's dynamic plugin loader.
func GetName() string { return pluginName }

// Cleanup is required by Bifrost's dynamic plugin loader.
func Cleanup() error { return nil }

// HTTPTransportPostHook verifies buffered chat completions through the ProofPulse
// Go service. It fails open: if verification is unavailable, Bifrost returns its
// original successful response unchanged.
func HTTPTransportPostHook(_ *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponse) error {
	if req == nil || resp == nil || req.Method != http.MethodPost || !strings.HasSuffix(strings.TrimRight(req.Path, "/"), "/chat/completions") {
		return nil
	}
	if strings.EqualFold(req.CaseInsensitiveHeaderLookup("X-ProofPulse-Internal"), "true") {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || len(resp.Body) == 0 {
		return nil
	}

	var request chatRequest
	if err := json.Unmarshal(req.Body, &request); err != nil || request.Stream {
		setStatus(resp, "skipped")
		return nil
	}
	question := userQuestion(request.Messages)
	answer, err := completionAnswer(resp.Body)
	if err != nil || len(question) < 3 || strings.TrimSpace(answer) == "" {
		setStatus(resp, "skipped")
		return nil
	}

	result, err := verifyThroughService(question, answer)
	if err != nil {
		setStatus(resp, "unavailable")
		return nil
	}
	if strings.TrimSpace(result.VerifiedAnswer) == "" {
		setStatus(resp, "unchanged")
		return nil
	}
	updatedAnswer := appendCitations(result.VerifiedAnswer, result.Verifications)
	updatedBody, err := replaceCompletionContent(resp.Body, updatedAnswer)
	if err != nil {
		setStatus(resp, "unchanged")
		return nil
	}
	resp.Body = updatedBody
	setStatus(resp, "verified")
	return nil
}

func verifyThroughService(question, answer string) (verifyResponse, error) {
	configMu.RLock()
	serviceURL := config.ServiceURL
	activeClient := client
	configMu.RUnlock()

	body, err := json.Marshal(verifyRequest{Question: question, Answer: answer})
	if err != nil {
		return verifyResponse{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), activeClient.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, serviceURL+"/api/verify", bytes.NewReader(body))
	if err != nil {
		return verifyResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := activeClient.Do(req)
	if err != nil {
		return verifyResponse{}, err
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return verifyResponse{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return verifyResponse{}, fmt.Errorf("ProofPulse returned HTTP %d", response.StatusCode)
	}
	var result verifyResponse
	if err := json.Unmarshal(responseBody, &result); err != nil {
		return verifyResponse{}, err
	}
	return result, nil
}

func userQuestion(messages []struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}) string {
	var parts []string
	for _, message := range messages {
		if message.Role != "user" {
			continue
		}
		if text := textContent(message.Content); strings.TrimSpace(text) != "" {
			parts = append(parts, strings.TrimSpace(text))
		}
	}
	return strings.Join(parts, "\n\n")
}

func textContent(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil {
		return ""
	}
	var builder strings.Builder
	for _, part := range parts {
		if part.Type == "text" || part.Type == "input_text" || part.Type == "" {
			builder.WriteString(part.Text)
		}
	}
	return builder.String()
}

func completionAnswer(body []byte) (string, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return "", err
	}
	var choices []json.RawMessage
	if err := json.Unmarshal(envelope["choices"], &choices); err != nil || len(choices) == 0 {
		return "", errors.New("response contains no chat choices")
	}
	var choice map[string]json.RawMessage
	if err := json.Unmarshal(choices[0], &choice); err != nil {
		return "", err
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(choice["message"], &message); err != nil {
		return "", err
	}
	answer := textContent(message["content"])
	if strings.TrimSpace(answer) == "" {
		return "", errors.New("response has no text content")
	}
	return answer, nil
}

func replaceCompletionContent(body []byte, content string) ([]byte, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}
	var choices []json.RawMessage
	if err := json.Unmarshal(envelope["choices"], &choices); err != nil || len(choices) == 0 {
		return nil, errors.New("response contains no chat choices")
	}
	var choice map[string]json.RawMessage
	if err := json.Unmarshal(choices[0], &choice); err != nil {
		return nil, err
	}
	var message map[string]json.RawMessage
	if err := json.Unmarshal(choice["message"], &message); err != nil {
		return nil, err
	}
	encodedContent, err := json.Marshal(content)
	if err != nil {
		return nil, err
	}
	message["content"] = encodedContent
	choice["message"], err = json.Marshal(message)
	if err != nil {
		return nil, err
	}
	choices[0], err = json.Marshal(choice)
	if err != nil {
		return nil, err
	}
	envelope["choices"], err = json.Marshal(choices)
	if err != nil {
		return nil, err
	}
	return json.Marshal(envelope)
}

func appendCitations(answer string, verifications []struct {
	Claim   string `json:"claim"`
	Status  string `json:"status"`
	Sources []struct {
		Title string `json:"title"`
		URL   string `json:"url"`
	} `json:"sources"`
}) string {
	var lines []string
	for _, item := range verifications {
		if len(item.Sources) == 0 {
			continue
		}
		claim := strings.TrimSpace(item.Claim)
		if claim == "" {
			claim = "Checked claim"
		}
		line := fmt.Sprintf("- **%s:** %s", item.Status, claim)
		for _, source := range item.Sources {
			parsed, err := url.Parse(source.URL)
			if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				continue
			}
			title := strings.TrimSpace(source.Title)
			if title == "" {
				title = "Source"
			}
			line += fmt.Sprintf(" [%s](%s)", title, parsed.String())
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return answer
	}
	return strings.TrimSpace(answer) + "\n\n**ProofPulse freshness check**\n" + strings.Join(lines, "\n")
}

func setStatus(resp *schemas.HTTPResponse, status string) {
	if resp.Headers == nil {
		resp.Headers = make(map[string]string)
	}
	resp.Headers["X-ProofPulse-Status"] = status
}
