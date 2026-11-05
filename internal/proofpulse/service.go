package proofpulse

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

	"proofpulse/internal/config"
)

const maxClaims = 3

type Service struct {
	config     config.Config
	httpClient *http.Client
}

func NewService(cfg config.Config) *Service {
	return &Service{
		config: cfg,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

func (s *Service) Verify(ctx context.Context, request VerifyRequest) (VerifyResponse, error) {
	draft := strings.TrimSpace(request.Answer)
	if draft == "" {
		var err error
		draft, err = s.chat(ctx, []chatMessage{
			{Role: "system", Content: "Answer clearly and concisely. Do not invent citations. State uncertainty when needed."},
			{Role: "user", Content: request.Question},
		}, 0.2)
		if err != nil {
			return VerifyResponse{}, fmt.Errorf("Bifrost could not generate the draft answer: %w", err)
		}
	}

	claims, err := s.extractClaims(ctx, draft)
	if err != nil {
		return VerifyResponse{}, fmt.Errorf("Bifrost could not extract claims: %w", err)
	}
	if len(claims) > maxClaims {
		claims = claims[:maxClaims]
	}

	verifications := make([]Verification, len(claims))
	var group sync.WaitGroup
	limit := make(chan struct{}, 3)
	for index, claim := range claims {
		group.Add(1)
		go func(index int, claim string) {
			defer group.Done()
			limit <- struct{}{}
			defer func() { <-limit }()
			verifications[index] = s.verifyClaim(ctx, claim)
		}(index, claim)
	}
	group.Wait()

	verifiedAnswer := s.repairAnswer(ctx, draft, verifications)
	sourcesUsed := 0
	for _, result := range verifications {
		sourcesUsed += len(result.Sources)
	}
	note := "Model calls are routed through Bifrost; Tavily evidence is checked per claim, with unclear results marked uncertain."
	if len(verifications) == 0 {
		note = "No time-sensitive claims were extracted; model traffic was routed through Bifrost."
	}

	return VerifyResponse{
		DraftAnswer:      draft,
		VerifiedAnswer:   verifiedAnswer,
		Verifications:    verifications,
		ClaimsChecked:    len(verifications),
		SourcesUsed:      sourcesUsed,
		VerificationNote: note,
	}, nil
}

func (s *Service) verifyClaim(ctx context.Context, claim string) Verification {
	sources, err := s.searchTavily(ctx, claim)
	if err != nil {
		return Verification{
			Claim:       claim,
			Status:      "uncertain",
			Explanation: "Live source lookup failed; ProofPulse is preserving the original claim without assigning a freshness verdict.",
			Sources:     []Source{},
		}
	}

	verdict, err := s.assessClaim(ctx, claim, sources)
	if err != nil {
		return Verification{
			Claim:       claim,
			Status:      "uncertain",
			Explanation: "The live sources were retrieved, but the model could not assess them; the original claim is preserved.",
			Sources:     sources,
		}
	}
	verdict.Claim = claim
	verdict.Sources = sources
	return verdict
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatCompletionRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
}

type chatCompletionResponse struct {
	Choices []struct {
		Message struct {
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func (s *Service) chat(ctx context.Context, messages []chatMessage, temperature float64) (string, error) {
	payload, err := json.Marshal(chatCompletionRequest{
		Model:       s.config.BifrostModel,
		Messages:    messages,
		Temperature: temperature,
	})
	if err != nil {
		return "", err
	}
	endpoint := strings.TrimRight(s.config.BifrostBaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.config.BifrostAPIKey)
	// Bifrost's ProofPulse plugin recognizes this internal marker and skips its
	// post-response callback, preventing recursive verification calls.
	req.Header.Set("X-ProofPulse-Internal", "true")

	response, err := s.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return "", err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("Bifrost returned HTTP %d: %s", response.StatusCode, conciseBody(body))
	}
	var completion chatCompletionResponse
	if err := json.Unmarshal(body, &completion); err != nil {
		return "", fmt.Errorf("invalid chat completion response: %w", err)
	}
	if len(completion.Choices) == 0 {
		return "", errors.New("Bifrost returned no completion choices")
	}
	content, err := decodeMessageContent(completion.Choices[0].Message.Content)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(content) == "" {
		return "", errors.New("Bifrost returned an empty message")
	}
	return content, nil
}

func decodeMessageContent(raw json.RawMessage) (string, error) {
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		return text, nil
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &parts); err == nil {
		var builder strings.Builder
		for _, part := range parts {
			if part.Type == "text" || part.Type == "" {
				builder.WriteString(part.Text)
			}
		}
		return builder.String(), nil
	}
	return "", errors.New("Bifrost returned unsupported message content")
}

type claimsResult struct {
	Claims []string `json:"claims"`
}

func (s *Service) extractClaims(ctx context.Context, answer string) ([]string, error) {
	prompt := "Extract up to three factual claims from the answer that could change over time. Include claims about people and roles, prices, laws, versions, launches, dates, statistics, and current events. Prefer the three most important claims to verify. Return only JSON in this exact shape: {\"claims\":[\"claim one\"]}. If there are no time-sensitive claims, return {\"claims\":[]}.\n\nANSWER:\n" + answer
	raw, err := s.chat(ctx, []chatMessage{{Role: "user", Content: prompt}}, 0)
	if err != nil {
		return nil, err
	}
	var result claimsResult
	if err := json.Unmarshal([]byte(stripJSONFence(raw)), &result); err != nil {
		return nil, fmt.Errorf("claim extraction was not valid JSON: %w", err)
	}
	claims := make([]string, 0, min(len(result.Claims), maxClaims))
	for _, claim := range result.Claims {
		claim = strings.TrimSpace(claim)
		if claim != "" {
			claims = append(claims, claim)
			if len(claims) == maxClaims {
				break
			}
		}
	}
	return claims, nil
}

type tavilyRequest struct {
	APIKey       string `json:"api_key"`
	Query        string `json:"query"`
	SearchDepth  string `json:"search_depth"`
	MaxResults   int    `json:"max_results"`
	IncludeAnswer bool  `json:"include_answer"`
}

type tavilyResponse struct {
	Results []struct {
		Title   string `json:"title"`
		URL     string `json:"url"`
		Content string `json:"content"`
	} `json:"results"`
}

func (s *Service) searchTavily(ctx context.Context, claim string) ([]Source, error) {
	if s.config.TavilyAPIKey == "" {
		return nil, errors.New("TAVILY_API_KEY is missing")
	}
	payload, err := json.Marshal(tavilyRequest{
		APIKey:        s.config.TavilyAPIKey,
		Query:         "Verify this current factual claim: " + claim,
		SearchDepth:   "advanced",
		MaxResults:    3,
		IncludeAnswer: false,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.tavily.com/search", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	response, err := s.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("Tavily returned HTTP %d: %s", response.StatusCode, conciseBody(body))
	}
	var result tavilyResponse
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("invalid Tavily response: %w", err)
	}
	sources := make([]Source, 0, len(result.Results))
	for _, item := range result.Results {
		if strings.TrimSpace(item.URL) == "" {
			continue
		}
		title := strings.TrimSpace(item.Title)
		if title == "" {
			title = "Untitled source"
		}
		excerpt := strings.TrimSpace(item.Content)
		if len(excerpt) > 600 {
			excerpt = excerpt[:600]
		}
		sourceURL, err := validSourceURL(item.URL)
		if err != nil {
			continue
		}
		sources = append(sources, Source{Title: title, URL: sourceURL, Excerpt: excerpt})
	}
	return sources, nil
}

func validSourceURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return "", errors.New("source URL must use HTTP or HTTPS")
	}
	return parsed.String(), nil
}

type claimVerdict struct {
	Status         string `json:"status"`
	FreshnessScore int    `json:"freshness_score"`
	Explanation    string `json:"explanation"`
	CorrectedClaim string `json:"corrected_claim"`
}

func (s *Service) assessClaim(ctx context.Context, claim string, sources []Source) (Verification, error) {
	evidence, err := json.Marshal(sources)
	if err != nil {
		return Verification{}, err
	}
	prompt := "You are a rigorous fact verifier. Compare a claim with live-source evidence. Return only JSON with exactly these keys: status, freshness_score, explanation, corrected_claim. status must be verified, stale, unsupported, or uncertain. freshness_score is an integer from 0 to 100 and represents confidence that the claim is current. Use stale only when evidence clearly contradicts an old claim. corrected_claim should be an empty string unless a specific correction is supported.\n\nCLAIM:\n" + claim + "\n\nEVIDENCE:\n" + string(evidence)
	raw, err := s.chat(ctx, []chatMessage{{Role: "user", Content: prompt}}, 0)
	if err != nil {
		return Verification{}, err
	}
	var verdict claimVerdict
	if err := json.Unmarshal([]byte(stripJSONFence(raw)), &verdict); err != nil {
		return Verification{}, fmt.Errorf("claim verdict was not valid JSON: %w", err)
	}
	switch verdict.Status {
	case "verified", "stale", "unsupported", "uncertain":
	default:
		verdict.Status = "uncertain"
	}
	if verdict.FreshnessScore < 0 {
		verdict.FreshnessScore = 0
	}
	if verdict.FreshnessScore > 100 {
		verdict.FreshnessScore = 100
	}
	if strings.TrimSpace(verdict.Explanation) == "" {
		verdict.Explanation = "The available sources did not produce a confident verdict."
	}
	return Verification{
		Status:         verdict.Status,
		FreshnessScore: verdict.FreshnessScore,
		Explanation:    verdict.Explanation,
		CorrectedClaim: strings.TrimSpace(verdict.CorrectedClaim),
	}, nil
}

func (s *Service) repairAnswer(ctx context.Context, answer string, verifications []Verification) string {
	corrections := make([]map[string]string, 0)
	for _, result := range verifications {
		if result.Status == "stale" && result.CorrectedClaim != "" {
			corrections = append(corrections, map[string]string{"original": result.Claim, "replacement": result.CorrectedClaim})
		}
	}
	if len(corrections) == 0 {
		return answer
	}
	encoded, err := json.Marshal(corrections)
	if err != nil {
		return answer
	}
	prompt := "Rewrite the answer only where corrections are supported. Keep its useful content concise. Do not invent citations or add facts beyond the supplied corrections.\n\nORIGINAL ANSWER:\n" + answer + "\n\nCORRECTIONS:\n" + string(encoded)
	rewritten, err := s.chat(ctx, []chatMessage{{Role: "user", Content: prompt}}, 0)
	if err != nil || strings.TrimSpace(rewritten) == "" {
		return answer
	}
	return rewritten
}

func stripJSONFence(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "```") {
		if newline := strings.IndexByte(value, '\n'); newline >= 0 {
			value = value[newline+1:]
		}
		value = strings.TrimSpace(strings.TrimSuffix(value, "```"))
	}
	return strings.TrimSpace(value)
}

func conciseBody(body []byte) string {
	value := strings.TrimSpace(string(body))
	if len(value) > 400 {
		value = value[:400] + "…"
	}
	return value
}
