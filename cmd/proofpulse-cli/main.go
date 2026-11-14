// Command proofpulse-cli submits a question to a ProofPulse API and renders
// its answer and evidence review for a human using a terminal.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

type verifyRequest struct {
	Question string `json:"question"`
}

type source struct {
	Title string `json:"title"`
	URL   string `json:"url"`
}

type verification struct {
	Claim          string   `json:"claim"`
	Status         string   `json:"status"`
	FreshnessScore int      `json:"freshness_score"`
	Explanation    string   `json:"explanation"`
	CorrectedClaim string   `json:"corrected_claim,omitempty"`
	Sources        []source `json:"sources"`
}

type verifyResponse struct {
	DraftAnswer      string         `json:"draft_answer"`
	VerifiedAnswer   string         `json:"verified_answer"`
	Verifications    []verification `json:"verifications"`
	ClaimsChecked    int            `json:"claims_checked"`
	SourcesUsed      int            `json:"sources_used"`
	VerificationNote string         `json:"verification_note"`
}

func main() {
	server := flag.String("server", os.Getenv("PROOFPULSE_API_URL"), "team-hosted ProofPulse API URL (or set PROOFPULSE_API_URL)")
	timeout := flag.Duration("timeout", 5*time.Minute, "maximum time to wait for verification")
	jsonOutput := flag.Bool("json", false, "print the raw JSON response")
	flag.Usage = func() {
		fmt.Fprintf(flag.CommandLine.Output(), "Usage: proofpulse-cli [-server API_URL] [-timeout 5m] [-json] QUESTION\n\n")
		fmt.Fprintf(flag.CommandLine.Output(), "Send a question to ProofPulse and display its answer, freshness findings, and evidence.\n")
		flag.PrintDefaults()
	}
	flag.Parse()

	if strings.TrimSpace(*server) == "" {
		fatal("ProofPulse API URL is not set. Use -server https://<team-api-host> or set PROOFPULSE_API_URL.")
	}
	question := strings.TrimSpace(strings.Join(flag.Args(), " "))
	if len(question) < 3 {
		flag.Usage()
		os.Exit(2)
	}
	endpoint, err := verifyEndpoint(*server)
	if err != nil {
		fatal("invalid ProofPulse API URL: " + err.Error())
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	payload, err := json.Marshal(verifyRequest{Question: question})
	if err != nil {
		fatal("could not encode question: " + err.Error())
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		fatal("could not create request: " + err.Error())
	}
	request.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: *timeout}
	response, err := client.Do(request)
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			fatal("request timed out; try again later")
		}
		fatal("could not reach ProofPulse API: " + err.Error())
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		fatal("could not read ProofPulse response: " + err.Error())
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var apiError struct {
			Detail string `json:"detail"`
		}
		_ = json.Unmarshal(body, &apiError)
		if apiError.Detail == "" {
			apiError.Detail = strings.TrimSpace(string(body))
		}
		fatal(fmt.Sprintf("ProofPulse API returned HTTP %d: %s", response.StatusCode, apiError.Detail))
	}

	if *jsonOutput {
		fmt.Println(string(body))
		return
	}
	var result verifyResponse
	if err := json.Unmarshal(body, &result); err != nil {
		fatal("ProofPulse returned an unexpected response: " + err.Error())
	}
	printReview(result)
}

func verifyEndpoint(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", fmt.Errorf("expected an http(s) URL, got %q", raw)
	}
	path := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(path, "/api/verify") {
		path += "/api/verify"
	}
	parsed.Path = path
	return parsed.String(), nil
}

func printReview(result verifyResponse) {
	answer := strings.TrimSpace(result.VerifiedAnswer)
	if answer == "" {
		answer = strings.TrimSpace(result.DraftAnswer)
	}
	fmt.Println("PROOFPULSE")
	fmt.Println("==========")
	fmt.Println("ANSWER")
	if answer == "" {
		fmt.Println("No answer was returned.")
	} else {
		fmt.Println(answer)
	}
	fmt.Println()

	needsReview := 0
	for _, finding := range result.Verifications {
		if finding.Status == "stale" || finding.Status == "unsupported" || finding.Status == "uncertain" {
			needsReview++
		}
	}
	fmt.Printf("FRESHNESS REVIEW  |  %d claims checked  |  %d need review  |  %d sources\n", result.ClaimsChecked, needsReview, result.SourcesUsed)
	if len(result.Verifications) == 0 {
		fmt.Println("No time-sensitive claims were checked.")
		if note := strings.TrimSpace(result.VerificationNote); note != "" {
			fmt.Println(note)
		}
		return
	}

	for index, finding := range result.Verifications {
		fmt.Printf("\n%d. %s", index+1, strings.ToUpper(finding.Status))
		if finding.FreshnessScore > 0 {
			fmt.Printf("  (%d%% freshness)", finding.FreshnessScore)
		}
		fmt.Printf("\n   Claim: %s\n", finding.Claim)
		if explanation := strings.TrimSpace(finding.Explanation); explanation != "" {
			fmt.Printf("   Why: %s\n", explanation)
		}
		if correction := strings.TrimSpace(finding.CorrectedClaim); correction != "" {
			fmt.Printf("   Correction: %s\n", correction)
		}
		if len(finding.Sources) > 0 {
			fmt.Println("   Sources:")
			for _, item := range finding.Sources {
				title := strings.TrimSpace(item.Title)
				if title == "" {
					title = item.URL
				}
				fmt.Printf("   - %s\n     %s\n", title, item.URL)
			}
		}
	}
	if note := strings.TrimSpace(result.VerificationNote); note != "" {
		fmt.Printf("\nNote: %s\n", note)
	}
}

func fatal(message string) {
	fmt.Fprintln(os.Stderr, "ProofPulse:", message)
	os.Exit(1)
}
