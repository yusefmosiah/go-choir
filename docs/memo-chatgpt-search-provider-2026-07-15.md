# ChatGPT/Codex Web Search Provider Gap

**Date:** 2026-07-15  
**Mutation class:** red  
**Protected surfaces:** ChatGPT OAuth refresh, external provider calls, normalized search evidence

## Problem

Choir's canonical search plane supports SearXNG, Tavily, Brave, Parallel, Exa,
Serper, and SerpAPI, but it cannot use the ChatGPT/Codex subscription already
configured for the gateway. The gateway already owns Codex OAuth loading and
refresh through `internal/provider.ChatGPTAuth`; search routing should reuse
that authority rather than introduce a second credential store or shell out to
the Codex CLI.

The missing adapter prevents a configured ChatGPT account from contributing
web results and citations to `POST /provider/v1/search`. Patching researcher
prompts would be a symptom-level workaround because search provider selection
is gateway infrastructure.

## Evidence

OpenAI's current web-search guide specifies the Responses API `web_search` tool,
`web_search_call` output items, and `url_citation` annotations:
<https://developers.openai.com/api/docs/guides/tools-web-search>.

A local, redacted probe using the existing `~/.codex/auth.json` access token and
`https://chatgpt.com/backend-api/codex/responses` established:

- `gpt-5.6` is rejected for ChatGPT-account Codex traffic;
- `gpt-5.5` returned HTTP 200 with `tools: [{"type":"web_search"}]`;
- the SSE stream emitted `response.web_search_call.*`,
  `response.output_item.done`, and `response.completed`;
- the completed web-search item carried queried URLs in `action.sources`;
- the completed message carried clickable `url_citation` annotations with URL,
  title, and text offsets.

No repository code was changed before this problem record.

## Intended change

Add a gateway `SearchProvider` named `chatgpt` that:

1. reuses `internal/provider.ChatGPTAuth` and `CHATGPT_AUTH_PATH`;
2. sends a streaming Responses request with the hosted `web_search` tool;
3. defaults to the ChatGPT-supported `gpt-5.5` model and low reasoning;
4. converts cited URLs into Choir `SearchResult` values with deduplication and
   the requested result bound;
5. retries once after a 401 by forcing the existing OAuth refresh path;
6. sanitizes upstream failures and never exposes credentials or response bodies.

The provider registry remains the single authority for eligibility and the
search plane remains the single authority for routing, health, and cooldown.

## Success artifact

Focused gateway tests must prove request shape, citation parsing, result bounds,
401 refresh retry, and provider registration. A local live integration probe
must return at least one normalized result with a clickable URL through the
adapter.

## Outcome evidence

- `go test -short ./internal/gateway`: pass.
- `CHATGPT_AUTH_PATH=~/.codex/auth.json go test ./internal/gateway -run
  '^TestChatGPTSearchProviderIntegration$' -v`: pass against the live ChatGPT
  endpoint through Choir's search plane, with one `chatgpt` attempt and 1–3
  normalized cited results required by the test.
- The unfocused non-short gateway suite still invokes unrelated paid-provider
  integration tests; Tavily, Exa, Serper, and SerpAPI reported exhausted quota.

## Rollback

Revert the adapter, its registry entry, and deployment comments. Existing search
providers and health records remain valid because the new provider has an
independent `chatgpt` health key.

## Heresy and conjecture deltas

- **Heresy discovered:** none.
- **Heresy introduced:** none intended.
- **Heresy repaired:** none.
- **Conjecture:** ChatGPT's Codex Responses endpoint preserves hosted web-search
  output and citation metadata sufficiently for Choir's normalized search
  contract.
