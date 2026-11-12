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
	config   = pluginConfig{ServiceURL: "http://127.0.0.1:8000", TimeoutSec: 300}
	client   = &http.Client{Timeout: 300 * time.Second}
)

type chatRequest struct {
	Messages []struct {
		Role      string            `json:"role"`
		Content   json.RawMessage   `json:"content"`
		ToolCalls []json.RawMessage `json:"tool_calls"`
	} `json:"messages"`
	Stream     bool              `json:"stream"`
	Tools      []json.RawMessage `json:"tools"`
	N          int               `json:"n"`
	Modalities []string          `json:"modalities"`
}

type streamBufferContextKey struct{}

var streamBufferKey = streamBufferContextKey{}

type bufferedChatStream struct {
	mu               sync.Mutex
	bypass           bool
	finalized        bool
	question         string
	content          strings.Builder
	refusal          strings.Builder
	lastIndex        int
	lastFinishReason *string
	lastRole         *string
}

type verifyRequest struct {
	Question string `json:"question"`
	Answer   string `json:"answer"`
}

type verifyResponse struct {
	VerifiedAnswer string `json:"verified_answer"`
	ClaimsChecked  int    `json:"claims_checked"`
	Verifications  []struct {
		Claim          string `json:"claim"`
		Status         string `json:"status"`
		FreshnessScore int    `json:"freshness_score"`
		Explanation    string `json:"explanation"`
		CorrectedClaim string `json:"corrected_claim"`
		EvidenceURL    string `json:"evidence_url"`
		EvidenceQuote  string `json:"evidence_quote"`
		Sources        []struct {
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

// HTTPTransportPreHook captures the request details before Bifrost consumes the
// HTTP body to start a stream. Bifrost's later per-chunk hook intentionally
// receives a body-less HTTPRequest, so streaming state must be prepared here.
func HTTPTransportPreHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest) (*schemas.HTTPResponse, error) {
	if ctx == nil || req == nil || !isChatCompletionsRequest(req) || strings.EqualFold(req.CaseInsensitiveHeaderLookup("X-ProofPulse-Internal"), "true") {
		return nil, nil
	}
	var request chatRequest
	if err := json.Unmarshal(req.Body, &request); err != nil || !request.Stream {
		return nil, nil
	}
	question, eligible := eligibleStreamingQuestion(req, request)
	ctx.SetValue(streamBufferKey, &bufferedChatStream{question: question, bypass: !eligible})
	return nil, nil
}

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
	if err := json.Unmarshal(req.Body, &request); err != nil {
		setStatus(resp, "skipped")
		return nil
	}
	if request.Stream {
		// Streaming responses are handled by HTTPTransportStreamChunkHook.
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
	if result.ClaimsChecked == 0 {
		setStatus(resp, "no-claims")
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
	setStatus(resp, "checked")
	return nil
}

// HTTPTransportResponseHeadersHook marks stream requests before Bifrost commits
// response headers. The final checked verdict is reflected in the returned answer;
// headers cannot be changed after the stream begins.
func HTTPTransportResponseHeadersHook(_ *schemas.BifrostContext, req *schemas.HTTPRequest, resp *schemas.HTTPResponseMetadata) error {
	if req == nil || resp == nil || !isChatCompletionsRequest(req) || strings.EqualFold(req.CaseInsensitiveHeaderLookup("X-ProofPulse-Internal"), "true") {
		return nil
	}
	var request chatRequest
	if json.Unmarshal(req.Body, &request) != nil || !request.Stream {
		return nil
	}
	status := "skipped"
	if _, ok := eligibleStreamingQuestion(req, request); ok {
		status = "buffered"
	}
	resp.SetHeader("X-ProofPulse-Status", status)
	return nil
}

// HTTPTransportStreamChunkHook buffers eligible plain-text chat streams, verifies
// the complete answer once Bifrost marks the last chunk, then releases one final
// content delta. This prevents unverified text from reaching the caller first.
func HTTPTransportStreamChunkHook(ctx *schemas.BifrostContext, req *schemas.HTTPRequest, chunk *schemas.BifrostStreamChunk) (*schemas.BifrostStreamChunk, error) {
	if ctx == nil || req == nil || chunk == nil || !isChatCompletionsRequest(req) || strings.EqualFold(req.CaseInsensitiveHeaderLookup("X-ProofPulse-Internal"), "true") {
		return chunk, nil
	}

	state, ok := ctx.Value(streamBufferKey).(*bufferedChatStream)
	if !ok {
		// Bifrost doesn't copy the consumed request body into stream-hook requests.
		// If setup state is missing, preserve the original stream safely.
		return chunk, nil
	}
	if state.bypass {
		return chunk, nil
	}
	if chunk.BifrostChatResponse == nil {
		// This route should produce chat chunks. If Bifrost supplies a different
		// chunk shape before we have buffered text, leave it alone.
		state.mu.Lock()
		state.bypass = true
		state.mu.Unlock()
		return chunk, nil
	}

	state.mu.Lock()
	for _, choice := range chunk.BifrostChatResponse.Choices {
		if choice.Index != 0 {
			continue
		}
		state.lastIndex = choice.Index
		if choice.FinishReason != nil {
			value := *choice.FinishReason
			state.lastFinishReason = &value
		}
		if choice.ChatStreamResponseChoice == nil || choice.ChatStreamResponseChoice.Delta == nil {
			continue
		}
		delta := choice.ChatStreamResponseChoice.Delta
		if delta.Role != nil {
			value := *delta.Role
			state.lastRole = &value
		}
		if delta.Content != nil {
			state.content.WriteString(*delta.Content)
		}
		if delta.Refusal != nil {
			state.refusal.WriteString(*delta.Refusal)
		}
	}
	final := false
	if marker, ok := ctx.Value(schemas.BifrostContextKeyStreamEndIndicator).(bool); ok {
		final = marker
	}
	if !final || state.finalized {
		state.mu.Unlock()
		return nil, nil
	}
	state.finalized = true
	question := state.question
	answer := state.content.String()
	refusal := state.refusal.String()
	lastIndex := state.lastIndex
	var lastFinishReason, lastRole *string
	if state.lastFinishReason != nil {
		value := *state.lastFinishReason
		lastFinishReason = &value
	}
	if state.lastRole != nil {
		value := *state.lastRole
		lastRole = &value
	}
	state.mu.Unlock()

	verifiedAnswer := answer
	if strings.TrimSpace(answer) != "" {
		result, err := verifyThroughServiceContext(ctx, question, answer)
		if err == nil && result.ClaimsChecked > 0 && strings.TrimSpace(result.VerifiedAnswer) != "" {
			verifiedAnswer = appendCitations(result.VerifiedAnswer, result.Verifications)
		}
	}

	response := *chunk.BifrostChatResponse
	choice := schemas.BifrostResponseChoice{Index: lastIndex, FinishReason: lastFinishReason}
	if len(response.Choices) > 0 {
		choice = response.Choices[0]
		if choice.FinishReason == nil {
			choice.FinishReason = lastFinishReason
		}
	}
	delta := &schemas.ChatStreamResponseChoiceDelta{Role: lastRole}
	if verifiedAnswer != "" {
		delta.Content = &verifiedAnswer
	}
	if refusal != "" {
		delta.Refusal = &refusal
	}
	choice.ChatStreamResponseChoice = &schemas.ChatStreamResponseChoice{Delta: delta}
	response.Choices = []schemas.BifrostResponseChoice{choice}
	modified := *chunk
	modified.BifrostChatResponse = &response
	return &modified, nil
}

func isChatCompletionsRequest(req *schemas.HTTPRequest) bool {
	return req != nil && req.Method == http.MethodPost && strings.HasSuffix(strings.TrimRight(req.Path, "/"), "/chat/completions")
}

func eligibleStreamingQuestion(req *schemas.HTTPRequest, request chatRequest) (string, bool) {
	if !request.Stream || len(request.Tools) != 0 || request.N > 1 || request.N < 0 {
		return "", false
	}
	for _, modality := range request.Modalities {
		if modality != "text" {
			return "", false
		}
	}
	for _, message := range request.Messages {
		if len(message.ToolCalls) != 0 || !isTextOnlyContent(message.Content) {
			return "", false
		}
	}
	question := userQuestion(request.Messages)
	return question, len(strings.TrimSpace(question)) >= 3
}

func isTextOnlyContent(raw json.RawMessage) bool {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return true
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) != nil || len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if part.Type != "text" && part.Type != "input_text" {
			return false
		}
	}
	return true
}

func verifyThroughService(question, answer string) (verifyResponse, error) {
	return verifyThroughServiceContext(context.Background(), question, answer)
}

func verifyThroughServiceContext(parent context.Context, question, answer string) (verifyResponse, error) {
	configMu.RLock()
	serviceURL := config.ServiceURL
	activeClient := client
	configMu.RUnlock()

	body, err := json.Marshal(verifyRequest{Question: question, Answer: answer})
	if err != nil {
		return verifyResponse{}, err
	}
	if parent == nil {
		parent = context.Background()
	}
	requestContext, cancel := context.WithTimeout(parent, activeClient.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(requestContext, http.MethodPost, serviceURL+"/api/verify", bytes.NewReader(body))
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
	Role      string            `json:"role"`
	Content   json.RawMessage   `json:"content"`
	ToolCalls []json.RawMessage `json:"tool_calls"`
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
	Claim          string `json:"claim"`
	Status         string `json:"status"`
	FreshnessScore int    `json:"freshness_score"`
	Explanation    string `json:"explanation"`
	CorrectedClaim string `json:"corrected_claim"`
	EvidenceURL    string `json:"evidence_url"`
	EvidenceQuote  string `json:"evidence_quote"`
	Sources        []struct {
		Title string `json:"title"`
		URL   string `json:"url"`
	} `json:"sources"`
}) string {
	var reports []string
	for index, item := range verifications {
		claim := cleanDisplayText(item.Claim)
		if claim == "" {
			claim = "Checked claim"
		}

		status := "Needs review"
		switch strings.ToLower(strings.TrimSpace(item.Status)) {
		case "verified":
			status = "Verified"
		case "stale":
			status = "Stale"
		case "unsupported":
			status = "Unsupported"
		}

		lines := []string{fmt.Sprintf("**%d. %s** · **Claim currentness: %d%%**", index+1, status, item.FreshnessScore), "- **Claim checked:** " + claim}
		if strings.EqualFold(strings.TrimSpace(item.Status), "stale") && strings.TrimSpace(item.CorrectedClaim) == "" {
			lines = append(lines, "- **Correction:** The evidence shows this claim is outdated, but does not establish the exact current value.")
		}
		if correction := cleanDisplayText(item.CorrectedClaim); correction != "" {
			lines = append(lines, "- **Evidence-supported correction:** "+correction)
		}
		if explanation := cleanDisplayText(item.Explanation); explanation != "" {
			lines = append(lines, "- **Why:** "+explanation)
		}
		if quote := cleanDisplayText(item.EvidenceQuote); quote != "" {
			lines = append(lines, "> **Evidence:** “"+quote+"”")
		}

		if evidenceURL := strings.TrimSpace(item.EvidenceURL); evidenceURL != "" {
			parsed, err := url.Parse(evidenceURL)
			if err == nil && parsed.Host != "" && (parsed.Scheme == "http" || parsed.Scheme == "https") {
				title := "Open supporting source"
				for _, source := range item.Sources {
					if strings.TrimSpace(source.URL) == evidenceURL && cleanDisplayText(source.Title) != "" {
						title = cleanDisplayText(source.Title)
						break
					}
				}
				lines = append(lines, "- **Source:** "+title+" — "+parsed.String())
			}
		}
		reports = append(reports, strings.Join(lines, "\n"))
	}
	if len(reports) == 0 {
		return strings.TrimSpace(answer)
	}

	needsReview := 0
	for _, item := range verifications {
		if !strings.EqualFold(strings.TrimSpace(item.Status), "verified") {
			needsReview++
		}
	}
	check := fmt.Sprintf("### ProofPulse freshness review\n\n%d claims checked; %d need review.\n\n%s", len(verifications), needsReview, strings.Join(reports, "\n\n"))
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return check
	}
	return answer + "\n\n---\n\n" + check
}
func cleanDisplayText(value string) string {
	value = strings.NewReplacer(
		"\\\\.", ".", "\\.", ".",
		"\\\\-", "-", "\\-", "-",
		"\\\\(", "(", "\\(", "(",
		"\\\\)", ")", "\\)", ")",
		"\\\\*", "*", "\\*", "*",
		"\\\\_", "_", "\\_", "_",
		"\\\\[", "[", "\\[", "[",
		"\\\\]", "]", "\\]", "]",
	).Replace(value)
	return strings.Join(strings.Fields(value), " ")
}

func setStatus(resp *schemas.HTTPResponse, status string) {
	if resp.Headers == nil {
		resp.Headers = make(map[string]string)
	}
	for key := range resp.Headers {
		if strings.EqualFold(key, "X-ProofPulse-Status") {
			delete(resp.Headers, key)
		}
	}
	resp.Headers["X-ProofPulse-Status"] = status
}
