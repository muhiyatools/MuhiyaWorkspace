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

// WebSource is a single ranked search result surfaced to both the model and UI.
type WebSource struct {
	Index       int     `json:"index"`
	Title       string  `json:"title"`
	URL         string  `json:"url"`
	Snippet     string  `json:"snippet"`
	PublishedAt string  `json:"publishedAt,omitempty"`
	Score       float64 `json:"score,omitempty"`
}

type WebSearchRequest struct {
	Query          string   `json:"query"`
	Recency        string   `json:"recency,omitempty"`
	MaxResults     int      `json:"maxResults,omitempty"`
	Topic          string   `json:"topic,omitempty"`
	IncludeDomains []string `json:"includeDomains,omitempty"`
	ExcludeDomains []string `json:"excludeDomains,omitempty"`
}

type WebSearchResult struct {
	Query    string      `json:"query"`
	Answer   string      `json:"answer,omitempty"`
	Provider string      `json:"provider"`
	Sources  []WebSource `json:"sources"`
	Content  string      `json:"content"`
}

type tavilyResponse struct {
	Query   string `json:"query"`
	Answer  string `json:"answer"`
	Results []struct {
		Title       string  `json:"title"`
		URL         string  `json:"url"`
		Content     string  `json:"content"`
		RawContent  string  `json:"raw_content"`
		PublishedAt string  `json:"published_date"`
		Score       float64 `json:"score"`
	} `json:"results"`
}

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

func (tc *ToolContext) execWebSearch(args map[string]interface{}) ToolExecution {
	result, err := tc.RunWebSearch(webSearchRequestFromArgs(args))
	if err != nil {
		return ToolExecution{LLMContent: "Web search failed: " + err.Error()}
	}
	return ToolExecution{
		LLMContent:  result.Content,
		ClientEvent: map[string]interface{}{"muhiya_sources": result.Sources},
	}
}

func (tc *ToolContext) RunWebSearch(req WebSearchRequest) (*WebSearchResult, error) {
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		return nil, fmt.Errorf("query is required")
	}
	req.MaxResults = clampSearchResults(req.MaxResults, tc.Complexity)
	req.Recency = normalizeRecency(req.Recency)
	req.Topic = normalizeTopic(req.Topic)
	req.IncludeDomains = cleanDomains(req.IncludeDomains, 300)
	req.ExcludeDomains = cleanDomains(req.ExcludeDomains, 150)

	var lastErr error
	if tc.Settings.TavilyAPIKey != "" {
		result, err := tc.tavilySearch(req)
		if err == nil && len(result.Sources) > 0 {
			return result, nil
		}
		lastErr = err
	}
	if tc.Settings.SerperAPIKey != "" {
		result, err := tc.serperSearch(req)
		if err == nil && len(result.Sources) > 0 {
			return result, nil
		}
		lastErr = err
	}

	ddg, answer := tc.duckDuckGoSearch(stripSiteFilters(req.Query), req.MaxResults)
	if len(ddg) > 0 {
		return buildWebSearchResult(req.Query, "duckduckgo", answer, ddg), nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("no search provider is configured")
}

func webSearchRequestFromArgs(args map[string]interface{}) WebSearchRequest {
	maxResults, _ := argInt(args, "maxResults")
	return WebSearchRequest{
		Query:          argString(args, "query"),
		Recency:        argString(args, "recency"),
		MaxResults:     maxResults,
		Topic:          argString(args, "topic"),
		IncludeDomains: argStringSlice(args, "includeDomains"),
		ExcludeDomains: argStringSlice(args, "excludeDomains"),
	}
}

func (tc *ToolContext) tavilySearch(req WebSearchRequest) (*WebSearchResult, error) {
	payload := map[string]interface{}{
		"query":               req.Query,
		"topic":               req.Topic,
		"search_depth":        "basic",
		"max_results":         req.MaxResults,
		"include_answer":      true,
		"include_raw_content": false,
	}
	if req.Recency != "any" {
		payload["time_range"] = req.Recency
	}
	if len(req.IncludeDomains) > 0 {
		payload["include_domains"] = req.IncludeDomains
	}
	if len(req.ExcludeDomains) > 0 {
		payload["exclude_domains"] = req.ExcludeDomains
	}

	body, _ := json.Marshal(payload)
	httpReq, err := http.NewRequest(http.MethodPost, "https://api.tavily.com/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Authorization", "Bearer "+tc.Settings.TavilyAPIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := toolHTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("tavily search provider unreachable")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("tavily returned status %d: %s", resp.StatusCode, truncate(string(raw), 240))
	}

	var tr tavilyResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return nil, fmt.Errorf("could not parse tavily results")
	}

	var sources []WebSource
	for _, item := range tr.Results {
		if strings.TrimSpace(item.URL) == "" {
			continue
		}
		snippet := firstNonEmpty(item.Content, item.RawContent)
		sources = append(sources, WebSource{
			Index:       len(sources) + 1,
			Title:       strings.TrimSpace(item.Title),
			URL:         strings.TrimSpace(item.URL),
			Snippet:     truncate(strings.TrimSpace(snippet), 500),
			PublishedAt: strings.TrimSpace(item.PublishedAt),
			Score:       item.Score,
		})
		if len(sources) >= req.MaxResults {
			break
		}
	}
	return buildWebSearchResult(req.Query, "tavily", tr.Answer, sources), nil
}

func (tc *ToolContext) serperSearch(req WebSearchRequest) (*WebSearchResult, error) {
	payload := map[string]interface{}{
		"q":   req.Query,
		"num": req.MaxResults,
	}
	switch req.Recency {
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

	httpReq, err := http.NewRequest(http.MethodPost, "https://google.serper.dev/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("X-API-KEY", tc.Settings.SerperAPIKey)
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := toolHTTPClient.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("serper search provider unreachable")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("serper returned status %d", resp.StatusCode)
	}

	var sr serperResponse
	if err := json.Unmarshal(raw, &sr); err != nil {
		return nil, fmt.Errorf("could not parse serper results")
	}

	answer := ""
	if sr.AnswerBox != nil {
		answer = firstNonEmpty(sr.AnswerBox.Answer, sr.AnswerBox.Snippet)
	}
	if answer == "" && sr.KnowledgeGraph != nil {
		answer = sr.KnowledgeGraph.Description
	}
	return buildWebSearchResult(req.Query, "serper", answer, parseSerperOrganic(sr, req.MaxResults)), nil
}

func parseSerperOrganic(sr serperResponse, limit int) []WebSource {
	var sources []WebSource
	for _, o := range sr.Organic {
		if o.Link == "" {
			continue
		}
		snippet := strings.TrimSpace(o.Snippet)
		if o.Date != "" {
			snippet = strings.TrimSpace(o.Date) + " - " + snippet
		}
		sources = append(sources, WebSource{
			Index:   len(sources) + 1,
			Title:   strings.TrimSpace(o.Title),
			URL:     strings.TrimSpace(o.Link),
			Snippet: snippet,
		})
		if len(sources) >= limit {
			break
		}
	}
	return sources
}

func (tc *ToolContext) duckDuckGoSearch(query string, limit int) ([]WebSource, string) {
	u := "https://api.duckduckgo.com/?" + url.Values{
		"q":             {query},
		"format":        {"json"},
		"no_html":       {"1"},
		"no_redirect":   {"1"},
		"skip_disambig": {"1"},
	}.Encode()

	httpReq, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, ""
	}
	httpReq.Header.Set("Accept", "application/json")
	resp, err := toolHTTPClient.Do(httpReq)
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
	if ddg.AbstractText != "" && ddg.AbstractURL != "" {
		sources = append(sources, WebSource{
			Index:   1,
			Title:   ddg.Heading,
			URL:     ddg.AbstractURL,
			Snippet: ddg.AbstractText,
		})
	}
	for _, rt := range ddg.RelatedTopics {
		if rt.FirstURL == "" || rt.Text == "" {
			continue
		}
		sources = append(sources, WebSource{
			Index:   len(sources) + 1,
			Title:   truncate(rt.Text, 80),
			URL:     rt.FirstURL,
			Snippet: rt.Text,
		})
		if len(sources) >= limit {
			break
		}
	}
	return sources, ddg.AbstractText
}

func buildWebSearchResult(query, provider, answer string, sources []WebSource) *WebSearchResult {
	result := &WebSearchResult{
		Query:    query,
		Answer:   strings.TrimSpace(answer),
		Provider: provider,
		Sources:  sources,
	}
	result.Content = formatSearchContent(result)
	return result
}

func formatSearchContent(result *WebSearchResult) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Web search results for %q (provider: %s):\n", result.Query, result.Provider)
	if result.Answer != "" {
		fmt.Fprintf(&sb, "[Answer] %s\n", truncate(result.Answer, 700))
	}
	for _, s := range result.Sources {
		fmt.Fprintf(&sb, "[%d] %s - %s\n", s.Index, s.Title, s.URL)
		if s.PublishedAt != "" {
			fmt.Fprintf(&sb, "Published: %s\n", s.PublishedAt)
		}
		fmt.Fprintf(&sb, "%s\n", truncate(s.Snippet, 500))
	}
	sb.WriteString("\nGround your answer in these sources and cite them inline as [n].")
	return strings.TrimSpace(sb.String())
}

func clampSearchResults(value int, complexity string) int {
	if value <= 0 {
		switch complexity {
		case "simple":
			value = 3
		case "medium":
			value = 5
		default:
			value = 8
		}
	}
	if value < 1 {
		return 1
	}
	if value > 10 {
		return 10
	}
	return value
}

func normalizeRecency(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "day", "week", "month", "year":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "any"
	}
}

func normalizeTopic(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "news", "finance":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "general"
	}
}

func cleanDomains(values []string, limit int) []string {
	var out []string
	seen := map[string]bool{}
	for _, value := range values {
		value = strings.TrimSpace(strings.ToLower(value))
		value = strings.TrimPrefix(value, "https://")
		value = strings.TrimPrefix(value, "http://")
		value = strings.TrimSuffix(value, "/")
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
		if len(out) >= limit {
			break
		}
	}
	return out
}

func argStringSlice(args map[string]interface{}, key string) []string {
	raw, ok := args[key]
	if !ok {
		return nil
	}
	items, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	var out []string
	for _, item := range items {
		if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

func truncate(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	if limit <= 3 {
		return s[:limit]
	}
	return s[:limit-3] + "..."
}

func stripSiteFilters(query string) string {
	lower := strings.ToLower(query)
	idx := strings.Index(lower, " (site:")
	if idx != -1 {
		return strings.TrimSpace(query[:idx])
	}
	idx = strings.Index(lower, " site:")
	if idx != -1 {
		return strings.TrimSpace(query[:idx])
	}
	return query
}
