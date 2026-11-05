package proofpulse

type VerifyRequest struct {
	Question string `json:"question"`
	Answer   string `json:"answer,omitempty"`
}

type Source struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Excerpt string `json:"excerpt"`
}

type Verification struct {
	Claim          string   `json:"claim"`
	Status         string   `json:"status"`
	FreshnessScore int      `json:"freshness_score"`
	Explanation    string   `json:"explanation"`
	CorrectedClaim string   `json:"corrected_claim,omitempty"`
	Sources        []Source `json:"sources"`
}

type VerifyResponse struct {
	DraftAnswer      string         `json:"draft_answer"`
	VerifiedAnswer   string         `json:"verified_answer"`
	Verifications    []Verification `json:"verifications"`
	ClaimsChecked    int            `json:"claims_checked"`
	SourcesUsed      int            `json:"sources_used"`
	VerificationNote string         `json:"verification_note"`
}
