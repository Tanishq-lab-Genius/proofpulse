<div align="center">
  <img src="assets/proofpulse-mark.svg" alt="ProofPulse logo" width="88" height="88">
  <h1>ProofPulse</h1>
  <p><strong>Freshness checks and evidence for AI answers.</strong></p>
  <p>One tool that checks what may have changed, shows the evidence, and flags what needs a human review.</p>
  <p>
    <a href="https://proofpulse-cy4nsmwp.manus.space">Product site</a>
    &nbsp;·&nbsp;
    <a href="#how-proofpulse-works">How it works</a>
    &nbsp;·&nbsp;
    <a href="#roadmap">Roadmap</a>
  </p>
  <p>
    <a href="https://github.com/Tanishq-lab-Genius/proofpulse/actions/workflows/go-build.yml"><img src="https://github.com/Tanishq-lab-Genius/proofpulse/actions/workflows/go-build.yml/badge.svg?branch=main" alt="Go build"></a>
    <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-54E0BE.svg" alt="MIT license"></a>
  </p>
</div>

---

> **Hackathon prototype.** ProofPulse helps people inspect time-sensitive claims and their sources. A status is an evidence signal, not a guarantee of truth; review the cited material before relying on consequential answers.

## The problem

AI answers can sound certain after a price, software release, policy, or other fact has changed. People need more than a fluent answer: they need to know which claims may have drifted and what current evidence says.

## The product

ProofPulse is a freshness-checking tool for AI answers. A user asks a question; ProofPulse gets an answer from the configured model, checks claims that may change, and returns the answer with claim-level findings and source links. The freshness check is the core workflow. Evidence accompanies the findings, including when a claim appears current; unclear or conflicting evidence stays marked for review.

For the judge experience, the goal is a terminal command that accepts a question and prints the answer, freshness findings, and evidence. Judges should not need OpenRouter, model-provider, Bifrost, or Tavily credentials. Those provider credentials belong in a ProofPulse service controlled by the project team; the terminal client sends only the question to that service.

## How ProofPulse works

```mermaid
flowchart LR
    Person[Person asks a question] --> ProofPulse[ProofPulse]
    ProofPulse --> Gateway[Bifrost routes the model request]
    Gateway --> Model[Configured model]
    Model --> ProofPulse
    ProofPulse --> Search[Tavily finds current evidence]
    Search --> ProofPulse
    ProofPulse --> Result[Answer, freshness findings, and sources]
```

ProofPulse checks claims individually and labels them `verified`, `stale`, `unsupported`, or `uncertain`. It preserves the original answer when the evidence is unclear instead of presenting a guess as a correction. The Bifrost plugin can apply the same freshness review to eligible model responses routed through the gateway.

## Run ProofPulse from Terminal

### Judge setup (keyless client)

The judge flow uses the team-hosted ProofPulse API, so it does not ask judges to create provider accounts or paste secrets. The CLI is in this repository. Once the team publishes the API URL, a judge can run:

```bash
git clone https://github.com/Tanishq-lab-Genius/proofpulse.git
cd proofpulse
go run ./cmd/proofpulse-cli -server "https://<TEAM-PROOFPULSE-API-HOST>" "Is Node.js 20 still supported, and which Node.js release line is Active LTS?"
```

Replace the host placeholder with the public HTTPS API address supplied by the project team. The command sends a question-only request and prints the generated answer, per-claim freshness status, explanation, correction when available, and source links. No model, Bifrost, or Tavily key is entered in the judge's terminal.

**Readiness:** the repository currently has the local ProofPulse API and Docker stack, but the shared, team-hosted API endpoint is not deployed yet. Until it is deployed and its URL is published, this judge command is not ready to run. Do not substitute `localhost` or a temporary tunnel: those point to one computer and are unavailable when it is off.

### Maintainer self-hosted setup

To run the entire service and Bifrost locally, maintainers can follow [DOCKER.md](DOCKER.md). That path requires the project operator's provider credentials in a private `.env.docker` file. It is not the keyless judge path above.

## Demo status

The Go backend supports a question-only request and can generate an answer, search evidence, and return structured findings using credentials configured by the operator. The website is a simulated product concept; it is not the required live demo. The judge demo is intended to run from Terminal through the keyless CLI and the team's hosted API. Hosting that API and publishing its URL are still required before judges can use this flow.

## What the prototype demonstrates

- A question can produce an answer without the user supplying an answer or API key.
- Claims likely to change are checked against current web evidence.
- Each finding carries an explanation and source links for inspection.
- Current-looking, stale, unsupported, and unclear claims are distinguished.
- The Bifrost gateway and its ProofPulse plugin keep the model request in one routed workflow.

## Where it can help

| Area | Example time-sensitive claims | Evidence that matters |
| --- | --- | --- |
| Software and developer support | Current versions, deprecations, API behavior, security notices | Official documentation, release notes, and advisories |
| Customer support and commerce | Prices, plan terms, product features, and availability | Current product pages and published policies |
| Legal and regulatory research | Case status, cited decisions, statutes, and rule changes | Primary legal sources, jurisdiction, and date; expert review remains necessary |
| Journalism, education, and history | Dates, attributed statements, and changing interpretations | Original documents, archives, studies, and dated reporting |
| Finance and compliance | Filings, rates, eligibility, and policy updates | Authoritative sources and human approval for consequential decisions |
| Healthcare and public health | Current guidance and public notices | Authoritative health sources and qualified professional review |

These are promising applications, not a claim that the prototype is validated for every domain. The strongest initial demonstrations are software documentation and customer-support facts, where source dates and changes are easy to show. The same freshness-first workflow can also return evidence for history or case research.

## Roadmap

The roadmap keeps one product focus: **check the freshness of an answer and show the evidence that supports the review.** The first delivery priority is a dependable terminal demo for judges; the website remains a simulated concept for the product's future direction.

### 1. Deliver the keyless Terminal judge flow

Deploy the ProofPulse API behind a stable HTTPS address, configure the project's model-provider and Tavily credentials in the host's private secret settings, and apply request and spend limits. Publish the API address with the repository's terminal CLI instructions. Judges then send questions from Terminal without provider keys. This shared-service flow, not the current local Docker setup, is the keyless judge experience.

### 2. Bring ProofPulse into compatible AI clients

After the terminal flow is reliable, offer the same freshness-first workflow through a remote MCP tool for compatible AI clients, alongside the Bifrost plugin for model traffic routed through the gateway. A client explicitly invokes ProofPulse with a question; ProofPulse returns the answer, freshness findings, and evidence. Provider and Tavily credentials remain managed by ProofPulse on the server. The hosted MCP tool is planned and is not live yet.

### 3. Make the hosted service dependable

Add user accounts, per-user request and spend limits, monitoring, privacy and retention controls, source-quality checks, and repeatable evaluations. Keep evidence visible and route unclear or high-impact claims to human review.

### 4. Scale and offer subscriptions

As use grows, meter questions, model usage, and evidence searches; enforce budgets and abuse limits; and add subscription tiers based on predictable usage allowances. ProofPulse would continue to use project-managed provider accounts, so subscribers would not need their own model or Tavily keys. A later service can separate request handling from background research, cache eligible repeated lookups with clear freshness windows, and scale workers as demand grows. These are future plans, not features represented as complete today.

## Current implementation

The repository contains the Go verification service, the Bifrost plugin, a local Docker configuration, and a Terminal CLI for calling a ProofPulse API. The local Docker preset uses OpenRouter as its configured model provider and Tavily for evidence search; it needs operator credentials. The CLI does not need provider credentials, but it needs a reachable ProofPulse API URL. The team-hosted API is not deployed yet, so the complete keyless judge flow is not yet available. Keep deployment credentials in the host's private secret store or local ignored environment file; never put them in the website, a public example, a CLI build, or a commit.

For maintainer-only local deployment notes, see [DOCKER.md](DOCKER.md). The Bifrost plugin source and its build notes are in [`bifrost-plugin/`](bifrost-plugin/).

## Project status and limits

ProofPulse is an early hackathon prototype. Claim extraction and model assessment can be imperfect, source search can return incomplete or conflicting material, and provider availability or quotas can affect a run. `uncertain` and `unsupported` are meaningful outcomes: check the evidence and do not treat the product as an automatic fact guarantee.

## License

ProofPulse is released under the [MIT License](LICENSE).
