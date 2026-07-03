package proxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// ============================================================================
// Web search tool executor.
//   Primary provider: Serper (https://google.serper.dev/search) — Google
//   results incl. answer boxes & knowledge graph, which is excellent for
//   sports, news and factual queries.
//   Fallback: DuckDuckGo Instant Answer API (keyless) for a best-effort
//   abstract when Serper is not configured or fails.
// ============================================================================

// WebSource is a single ranked search result surfaced to both the model and
// the UI (as a citation card).
type WebSource struct {
	Index   int    `json:"index"`
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// --- Serper response shapes -------------------------------------------------

type serperResponse struct {
	AnswerBox *struct {
		Title   string `json:"title"`
		Answer  string `json:"answer"`
		Snippet string `json:"snippet"`
		Link    string `json:"link"`
	} `json:"answerBox"`
	KnowledgeGraph *struct {
		Title           string `json:"title"`
		Type            string `json:"type"`
		Description     string `json:"description"`
		DescriptionLink string `json:"descriptionLink"`
	} `json:"knowledgeGraph"`
	Organic []struct {
		Title   string `json:"title"`
		Link    string `json:"link"`
		Snippet string `json:"snippet"`
		Date    string `json:"date"`
	} `json:"organic"`
}

// execWebSearch runs a web search and returns cited context for the model
// plus a muhiya_sources event for the UI.
func (tc *ToolContext) execWebSearch(args map[string]interface{}) ToolExecution {
	query := argString(args, "query")
	if query == "" {
		return ToolExecution{LLMContent: "No search query was provided."}
	}
	recency := strings.ToLower(argString(args, "recency"))

	var sources []WebSource
	var answer string
	var err error

	if tc.Settings.SerperAPIKey != "" {
		scopedQuery, isScoped := getScopedQuery(query)
		if isScoped {
			sources, answer, err = tc.serperSearch(scopedQuery, recency)
		}
		if !isScoped || err != nil || len(sources) == 0 {
			sources, answer, err = tc.serperSearch(query, recency)
		}
	} else {
		err = fmt.Errorf("no search provider configured")
	}

	// Fallback to DuckDuckGo if the primary provider failed or returned nothing.
	if (err != nil || len(sources) == 0) {
		limit := 8
		if tc.Complexity == "simple" {
			limit = 3
		} else if tc.Complexity == "medium" {
			limit = 5
		}
		if ddg, ans2 := tc.duckDuckGoSearch(query, limit); len(ddg) > 0 {
			sources = ddg
			if answer == "" {
				answer = ans2
			}
			err = nil
		}
	}

	if len(sources) == 0 {
		msg := "The web search returned no usable results."
		if err != nil {
			msg = "Web search failed: " + err.Error()
		}
		return ToolExecution{LLMContent: msg}
	}

	// Build the cited context block for the model.
	var sb strings.Builder
	fmt.Fprintf(&sb, "Web search results for %q:\n", query)
	if answer != "" {
		fmt.Fprintf(&sb, "[Answer] %s\n", truncate(answer, 500))
	}
	for _, s := range sources {
		fmt.Fprintf(&sb, "[%d] %s — %s\n%s\n", s.Index, s.Title, s.URL, truncate(s.Snippet, 400))
	}
	sb.WriteString("\nGround your answer in these sources and cite them inline as [n].")

	event := map[string]interface{}{
		"muhiya_sources": sources,
	}
	return ToolExecution{LLMContent: strings.TrimSpace(sb.String()), ClientEvent: event}
}

// serperSearch queries the Serper Google Search API.
func (tc *ToolContext) serperSearch(query, recency string) ([]WebSource, string, error) {
	limit := 8
	if tc.Complexity == "simple" {
		limit = 3
	} else if tc.Complexity == "medium" {
		limit = 5
	}

	payload := map[string]interface{}{
		"q":   query,
		"num": limit,
	}
	switch recency {
	case "day":
		payload["tbs"] = "qdr:d"
	case "week":
		payload["tbs"] = "qdr:w"
	case "month":
		payload["tbs"] = "qdr:m"
	case "year":
		payload["tbs"] = "qdr:y"
	}
	body, _ := json.Marshal(payload)

	req, err := http.NewRequest(http.MethodPost, "https://google.serper.dev/search", bytes.NewReader(body))
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("X-API-KEY", tc.Settings.SerperAPIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := toolHTTPClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("search provider unreachable")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode >= 400 {
		return nil, "", fmt.Errorf("search provider returned status %d", resp.StatusCode)
	}

	var sr serperResponse
	if err := json.Unmarshal(raw, &sr); err != nil {
		return nil, "", fmt.Errorf("could not parse search results")
	}

	var answer string
	if sr.AnswerBox != nil {
		if sr.AnswerBox.Answer != "" {
			answer = sr.AnswerBox.Answer
		} else if sr.AnswerBox.Snippet != "" {
			answer = sr.AnswerBox.Snippet
		}
	}
	if answer == "" && sr.KnowledgeGraph != nil && sr.KnowledgeGraph.Description != "" {
		answer = sr.KnowledgeGraph.Description
	}

	sources := parseSerperOrganic(sr)
	return sources, answer, nil
}

// parseSerperOrganic converts Serper organic results into ranked WebSources.
// Extracted as a pure function so it can be unit-tested without HTTP.
func parseSerperOrganic(sr serperResponse) []WebSource {
	var sources []WebSource
	idx := 1
	for _, o := range sr.Organic {
		if o.Link == "" {
			continue
		}
		snippet := o.Snippet
		if o.Date != "" {
			snippet = o.Date + " — " + snippet
		}
		sources = append(sources, WebSource{
			Index:   idx,
			Title:   strings.TrimSpace(o.Title),
			URL:     o.Link,
			Snippet: strings.TrimSpace(snippet),
		})
		idx++
		if idx > 8 {
			break
		}
	}
	return sources
}

// duckDuckGoSearch is a keyless best-effort fallback using the Instant Answer
// API. It only reliably yields an abstract + related topics, not full web
// results, but it keeps search working without a Serper key.
func (tc *ToolContext) duckDuckGoSearch(query string, limit int) ([]WebSource, string) {
	u := "https://api.duckduckgo.com/?" + url.Values{
		"q":                 {query},
		"format":            {"json"},
		"no_html":           {"1"},
		"no_redirect":       {"1"},
		"skip_disambig":     {"1"},
	}.Encode()

	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, ""
	}
	req.Header.Set("Accept", "application/json")
	resp, err := toolHTTPClient.Do(req)
	if err != nil {
		return nil, ""
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	var ddg struct {
		AbstractText  string `json:"AbstractText"`
		AbstractURL   string `json:"AbstractURL"`
		Heading       string `json:"Heading"`
		RelatedTopics []struct {
			Text     string `json:"Text"`
			FirstURL string `json:"FirstURL"`
		} `json:"RelatedTopics"`
	}
	if err := json.Unmarshal(raw, &ddg); err != nil {
		return nil, ""
	}

	var sources []WebSource
	idx := 1
	if ddg.AbstractText != "" && ddg.AbstractURL != "" {
		sources = append(sources, WebSource{
			Index:   idx,
			Title:   ddg.Heading,
			URL:     ddg.AbstractURL,
			Snippet: ddg.AbstractText,
		})
		idx++
	}
	for _, rt := range ddg.RelatedTopics {
		if rt.FirstURL == "" || rt.Text == "" {
			continue
		}
		sources = append(sources, WebSource{
			Index:   idx,
			Title:   truncate(rt.Text, 80),
			URL:     rt.FirstURL,
			Snippet: rt.Text,
		})
		idx++
		if idx > limit {
			break
		}
	}
	return sources, ddg.AbstractText
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	return s[:limit-3] + "..."
}

// getScopedQuery classifies the search query by matching keywords (English and Arabic)
// with word boundaries and returns a modified query targeting specific high-quality domains.
func getScopedQuery(query string) (string, bool) {
	lower := strings.ToLower(query)

	// 1. Biography, General Facts & Entities (Checked first to prioritize "who is <politician>")
	biographyKeywords := []string{
		"who is", "born", "died", "biography", "founder", "ceo", "actor", "history", "wikipedia", "britannica",
		"من هو", "من هي", "ولد", "توفي", "سيرة", "ويكيبيديا", "تاريخ", "مؤسس",
	}
	for _, k := range biographyKeywords {
		if containsWord(lower, k) {
			return query + " (site:wikipedia.org OR site:britannica.com OR site:imdb.com OR site:biography.com)", true
		}
	}

	// 2. Sports & Football
	sportsKeywords := []string{
		"football", "soccer", "match", "fixture", "standing", "league", "cup", "score", "vs", "versus", "player", "goals", "transfer",
		"كرة", "كورة", "الدوري", "ترتيب", "مباراة", "مباريات", "الاهلي", "الزمالك", "اهداف", "كاس",
	}
	for _, k := range sportsKeywords {
		if containsWord(lower, k) {
			return query + " (site:fifa.com OR site:uefa.com OR site:espn.com OR site:skysports.com OR site:goal.com OR site:whoscored.com OR site:transfermarkt.com OR site:kooora.com)", true
		}
	}

	// 3. Economy & Finance
	economyKeywords := []string{
		"gdp", "inflation", "economy", "economic", "tradingeconomics", "stock", "finance", "currency", "exchange", "revenue", "debt",
		"اقتصاد", "تضخم", "فائدة", "سعر", "اسهم", "بورصة", "عملة", "نمو", "ديون",
	}
	for _, k := range economyKeywords {
		if containsWord(lower, k) {
			return query + " (site:imf.org OR site:worldbank.org OR site:tradingeconomics.com OR site:bloomberg.com OR site:reuters.com OR site:investopedia.com OR site:yahoo.com)", true
		}
	}

	// 4. Politics & Global News
	politicsKeywords := []string{
		"politics", "political", "election", "president", "minister", "government", "parliament", "treaty", "war", "summit", "protest", "un", "united nations",
		"رئيس", "وزير", "حكومة", "انتخابات", "سياسة", "برلمان", "حرب", "معاهدة", "الامم المتحدة",
	}
	for _, k := range politicsKeywords {
		if containsWord(lower, k) {
			return query + " (site:reuters.com OR site:apnews.com OR site:bbc.com OR site:aljazeera.com OR site:cnn.com)", true
		}
	}

	// 5. Technology & Science
	techKeywords := []string{
		"technology", "ai", "software", "programming", "science", "physics", "chemistry", "space", "nasa", "scientific", "research", "paper", "github", "arxiv",
		"تكنولوجيا", "برمجة", "برنامج", "ذكاء اصطناعي", "فيزياء", "كيمياء", "فضاء", "ناسا", "علمي", "بحث",
	}
	for _, k := range techKeywords {
		if containsWord(lower, k) {
			return query + " (site:techcrunch.com OR site:wired.com OR site:theverge.com OR site:github.com OR site:nature.com OR site:arxiv.org)", true
		}
	}

	return query, false
}

func containsWord(s, word string) bool {
	idx := strings.Index(s, word)
	if idx == -1 {
		return false
	}
	for idx != -1 {
		startBound := idx == 0 || isBoundary(s[idx-1])
		endBound := idx+len(word) == len(s) || isBoundary(s[idx+len(word)])
		if startBound && endBound {
			return true
		}
		nextIdx := strings.Index(s[idx+1:], word)
		if nextIdx == -1 {
			break
		}
		idx = idx + 1 + nextIdx
	}
	return false
}

func isBoundary(b byte) bool {
	return b == ' ' || b == '.' || b == ',' || b == '?' || b == '!' || b == ';' || b == '-' || b == '_' || b == '(' || b == ')' || b == '/' || b == '\\'
}
