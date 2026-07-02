package proxy

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ============================================================================
// API-Football (api-sports.io) client + football tool executors.
//   Docs: https://www.api-football.com/documentation-v3
//   Auth: header  x-apisports-key: <key>   (direct api-sports.io host)
//   Base: https://v3.football.api-sports.io
// Free tier is 100 requests/day, so name->id lookups are cached per request
// and each executor makes as few calls as possible.
// ============================================================================

// --- Response envelope ------------------------------------------------------

type afEnvelope struct {
	Results  int             `json:"results"`
	Errors   json.RawMessage `json:"errors"` // [] on success, or {"key":"msg"} on error
	Response json.RawMessage `json:"response"`
}

// --- Shared sub-structures --------------------------------------------------

type afTeamRef struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Logo   string `json:"logo"`
	Winner *bool  `json:"winner"`
}

type afFixture struct {
	Fixture struct {
		ID     int    `json:"id"`
		Date   string `json:"date"`
		Status struct {
			Long    string `json:"long"`
			Short   string `json:"short"`
			Elapsed int    `json:"elapsed"`
		} `json:"status"`
		Venue struct {
			Name string `json:"name"`
			City string `json:"city"`
		} `json:"venue"`
	} `json:"fixture"`
	League struct {
		ID      int    `json:"id"`
		Name    string `json:"name"`
		Country string `json:"country"`
		Round   string `json:"round"`
		Season  int    `json:"season"`
	} `json:"league"`
	Teams struct {
		Home afTeamRef `json:"home"`
		Away afTeamRef `json:"away"`
	} `json:"teams"`
	Goals struct {
		Home *int `json:"home"`
		Away *int `json:"away"`
	} `json:"goals"`
}

type afTeamSearchItem struct {
	Team struct {
		ID      int    `json:"id"`
		Name    string `json:"name"`
		Country string `json:"country"`
		Logo    string `json:"logo"`
		Founded int    `json:"founded"`
	} `json:"team"`
	Venue struct {
		Name     string `json:"name"`
		City     string `json:"city"`
		Capacity int    `json:"capacity"`
	} `json:"venue"`
}

type afLeagueSearchItem struct {
	League struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
		Type string `json:"type"`
	} `json:"league"`
	Country struct {
		Name string `json:"name"`
	} `json:"country"`
	Seasons []struct {
		Year    int  `json:"year"`
		Current bool `json:"current"`
	} `json:"seasons"`
}

type afStandingRow struct {
	Rank      int       `json:"rank"`
	Team      afTeamRef `json:"team"`
	Points    int       `json:"points"`
	GoalsDiff int       `json:"goalsDiff"`
	Group     string    `json:"group"`
	Form      string    `json:"form"`
	All       struct {
		Played int `json:"played"`
		Win    int `json:"win"`
		Draw   int `json:"draw"`
		Lose   int `json:"lose"`
		Goals  struct {
			For     int `json:"for"`
			Against int `json:"against"`
		} `json:"goals"`
	} `json:"all"`
}

type afStandingsItem struct {
	League struct {
		ID        int               `json:"id"`
		Name      string            `json:"name"`
		Country   string            `json:"country"`
		Season    int               `json:"season"`
		Logo      string            `json:"logo"`
		Standings [][]afStandingRow `json:"standings"`
	} `json:"league"`
}

// --- Low-level HTTP ---------------------------------------------------------

// footballGet performs a GET against the API-Football API and decodes the
// envelope's response array into out. It returns a user-safe error string
// (never leaks the API key) when something goes wrong.
func (tc *ToolContext) footballGet(endpoint string, params url.Values, out interface{}) error {
	if tc.Settings.FootballAPIKey == "" {
		return fmt.Errorf("football API is not configured")
	}
	u := tc.Settings.FootballBaseURL + "/" + strings.TrimLeft(endpoint, "/")
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return fmt.Errorf("failed to build football request: %w", err)
	}
	req.Header.Set("x-apisports-key", tc.Settings.FootballAPIKey)
	req.Header.Set("Accept", "application/json")

	resp, err := toolHTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("football API unreachable: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20)) // cap at 4MB

	if resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("football API daily quota reached")
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("football API returned status %d", resp.StatusCode)
	}

	var env afEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("could not parse football API response")
	}
	// Errors field is [] on success; anything longer than an empty array/object
	// signals an API-level error (e.g. invalid key, bad params).
	if len(env.Errors) > 0 {
		trimmed := strings.TrimSpace(string(env.Errors))
		if trimmed != "[]" && trimmed != "{}" && trimmed != "null" {
			return fmt.Errorf("football API error: %s", truncate(trimmed, 200))
		}
	}
	if out != nil && len(env.Response) > 0 {
		if err := json.Unmarshal(env.Response, out); err != nil {
			return fmt.Errorf("could not decode football data")
		}
	}
	return nil
}

// --- Name resolution --------------------------------------------------------

func (tc *ToolContext) resolveTeamID(name string) (afTeamSearchItem, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	var items []afTeamSearchItem
	params := url.Values{}
	params.Set("search", name)
	if err := tc.footballGet("teams", params, &items); err != nil {
		return afTeamSearchItem{}, err
	}
	if len(items) == 0 {
		return afTeamSearchItem{}, fmt.Errorf("no team found matching %q", name)
	}
	// Prefer an exact (case-insensitive) name match, else the first result.
	best := items[0]
	for _, it := range items {
		if strings.EqualFold(it.Team.Name, name) {
			best = it
			break
		}
	}
	if tc.teamIDCache != nil {
		tc.teamIDCache[key] = best.Team.ID
	}
	return best, nil
}

func (tc *ToolContext) resolveLeague(name string) (afLeagueSearchItem, error) {
	var items []afLeagueSearchItem
	params := url.Values{}
	params.Set("search", name)
	if err := tc.footballGet("leagues", params, &items); err != nil {
		return afLeagueSearchItem{}, err
	}
	if len(items) == 0 {
		return afLeagueSearchItem{}, fmt.Errorf("no competition found matching %q", name)
	}
	best := items[0]
	for _, it := range items {
		if strings.EqualFold(it.League.Name, name) {
			best = it
			break
		}
	}
	return best, nil
}

// --- Executors --------------------------------------------------------------

func (tc *ToolContext) execFootballLiveScores(args map[string]interface{}) ToolExecution {
	params := url.Values{}
	params.Set("live", "all")
	leagueName := argString(args, "league")
	if leagueName != "" {
		if lg, err := tc.resolveLeague(leagueName); err == nil {
			params.Set("league", strconv.Itoa(lg.League.ID))
			params.Set("season", strconv.Itoa(currentFootballSeason()))
		}
	}
	var fixtures []afFixture
	if err := tc.footballGet("fixtures", params, &fixtures); err != nil {
		return ToolExecution{LLMContent: "Could not fetch live scores: " + err.Error()}
	}
	if len(fixtures) == 0 {
		return ToolExecution{LLMContent: "There are no live football matches right now."}
	}
	return tc.fixturesToExecution(fixtures, "live_scores", "Live football matches")
}

func (tc *ToolContext) execFootballFixtures(args map[string]interface{}) ToolExecution {
	params := url.Values{}
	date := argString(args, "date")
	team := argString(args, "team")
	league := argString(args, "league")
	when := strings.ToLower(argString(args, "when"))
	if when != "last" {
		when = "next"
	}

	switch {
	case date != "":
		params.Set("date", date)
	case team != "":
		ti, err := tc.resolveTeamID(team)
		if err != nil {
			return ToolExecution{LLMContent: err.Error()}
		}
		params.Set("team", strconv.Itoa(ti.Team.ID))
		if when == "last" {
			params.Set("last", "5")
		} else {
			params.Set("next", "5")
		}
	case league != "":
		lg, err := tc.resolveLeague(league)
		if err != nil {
			return ToolExecution{LLMContent: err.Error()}
		}
		params.Set("league", strconv.Itoa(lg.League.ID))
		params.Set("season", strconv.Itoa(currentFootballSeason()))
		if when == "last" {
			params.Set("last", "10")
		} else {
			params.Set("next", "10")
		}
	default:
		params.Set("date", time.Now().UTC().Format("2006-01-02"))
	}

	var fixtures []afFixture
	if err := tc.footballGet("fixtures", params, &fixtures); err != nil {
		return ToolExecution{LLMContent: "Could not fetch fixtures: " + err.Error()}
	}
	if len(fixtures) == 0 {
		return ToolExecution{LLMContent: "No fixtures found for that query."}
	}
	if len(fixtures) > 15 {
		fixtures = fixtures[:15]
	}
	return tc.fixturesToExecution(fixtures, "fixtures", "Football fixtures")
}

func (tc *ToolContext) execFootballStandings(args map[string]interface{}) ToolExecution {
	leagueName := argString(args, "league")
	if leagueName == "" {
		return ToolExecution{LLMContent: "Please specify a league for standings."}
	}
	lg, err := tc.resolveLeague(leagueName)
	if err != nil {
		return ToolExecution{LLMContent: err.Error()}
	}
	season := currentFootballSeason()
	if s, ok := argInt(args, "season"); ok && s > 1900 {
		season = s
	}
	params := url.Values{}
	params.Set("league", strconv.Itoa(lg.League.ID))
	params.Set("season", strconv.Itoa(season))

	var items []afStandingsItem
	if err := tc.footballGet("standings", params, &items); err != nil {
		return ToolExecution{LLMContent: "Could not fetch standings: " + err.Error()}
	}
	if len(items) == 0 || len(items[0].League.Standings) == 0 {
		return ToolExecution{LLMContent: fmt.Sprintf("No standings available for %s (%d).", lg.League.Name, season)}
	}

	table := items[0].League.Standings[0]
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s standings (season %d):\n", items[0].League.Name, items[0].League.Season)
	rows := make([]map[string]interface{}, 0, len(table))
	for _, r := range table {
		fmt.Fprintf(&sb, "%d. %s — %d pts (P%d W%d D%d L%d, GD %+d)\n",
			r.Rank, r.Team.Name, r.Points, r.All.Played, r.All.Win, r.All.Draw, r.All.Lose, r.GoalsDiff)
		rows = append(rows, map[string]interface{}{
			"rank":    r.Rank,
			"team":    r.Team.Name,
			"logo":    r.Team.Logo,
			"played":  r.All.Played,
			"win":     r.All.Win,
			"draw":    r.All.Draw,
			"lose":    r.All.Lose,
			"gf":      r.All.Goals.For,
			"ga":      r.All.Goals.Against,
			"gd":      r.GoalsDiff,
			"points":  r.Points,
			"form":    r.Form,
		})
	}

	event := map[string]interface{}{
		"muhiya_football": map[string]interface{}{
			"type":   "standings",
			"league": items[0].League.Name,
			"logo":   items[0].League.Logo,
			"season": items[0].League.Season,
			"table":  rows,
		},
	}
	return ToolExecution{LLMContent: strings.TrimSpace(sb.String()), ClientEvent: event}
}

func (tc *ToolContext) execFootballTeam(args map[string]interface{}) ToolExecution {
	name := argString(args, "name")
	if name == "" {
		return ToolExecution{LLMContent: "Please specify a team name."}
	}
	ti, err := tc.resolveTeamID(name)
	if err != nil {
		return ToolExecution{LLMContent: err.Error()}
	}
	// One extra call: the team's next 3 fixtures.
	params := url.Values{}
	params.Set("team", strconv.Itoa(ti.Team.ID))
	params.Set("next", "3")
	var fixtures []afFixture
	_ = tc.footballGet("fixtures", params, &fixtures)

	var sb strings.Builder
	fmt.Fprintf(&sb, "%s (%s)", ti.Team.Name, ti.Team.Country)
	if ti.Team.Founded > 0 {
		fmt.Fprintf(&sb, ", founded %d", ti.Team.Founded)
	}
	if ti.Venue.Name != "" {
		fmt.Fprintf(&sb, ", home stadium %s", ti.Venue.Name)
		if ti.Venue.Capacity > 0 {
			fmt.Fprintf(&sb, " (capacity %d)", ti.Venue.Capacity)
		}
	}
	sb.WriteString(".\n")
	upcoming := make([]map[string]interface{}, 0, len(fixtures))
	if len(fixtures) > 0 {
		sb.WriteString("Upcoming fixtures:\n")
		for _, f := range fixtures {
			sb.WriteString("- " + formatMatchLine(f) + "\n")
			upcoming = append(upcoming, fixtureCard(f))
		}
	}

	event := map[string]interface{}{
		"muhiya_football": map[string]interface{}{
			"type":     "team",
			"name":     ti.Team.Name,
			"country":  ti.Team.Country,
			"logo":     ti.Team.Logo,
			"founded":  ti.Team.Founded,
			"venue":    ti.Venue.Name,
			"capacity": ti.Venue.Capacity,
			"upcoming": upcoming,
		},
	}
	return ToolExecution{LLMContent: strings.TrimSpace(sb.String()), ClientEvent: event}
}

func (tc *ToolContext) execFootballHeadToHead(args map[string]interface{}) ToolExecution {
	name1 := argString(args, "team1")
	name2 := argString(args, "team2")
	if name1 == "" || name2 == "" {
		return ToolExecution{LLMContent: "Please specify both teams for head-to-head."}
	}
	t1, err := tc.resolveTeamID(name1)
	if err != nil {
		return ToolExecution{LLMContent: err.Error()}
	}
	t2, err := tc.resolveTeamID(name2)
	if err != nil {
		return ToolExecution{LLMContent: err.Error()}
	}
	params := url.Values{}
	params.Set("h2h", fmt.Sprintf("%d-%d", t1.Team.ID, t2.Team.ID))
	params.Set("last", "8")
	var fixtures []afFixture
	if err := tc.footballGet("fixtures/headtohead", params, &fixtures); err != nil {
		return ToolExecution{LLMContent: "Could not fetch head-to-head: " + err.Error()}
	}
	if len(fixtures) == 0 {
		return ToolExecution{LLMContent: fmt.Sprintf("No recent head-to-head matches between %s and %s.", t1.Team.Name, t2.Team.Name)}
	}
	exec := tc.fixturesToExecution(fixtures, "head_to_head",
		fmt.Sprintf("%s vs %s — recent meetings", t1.Team.Name, t2.Team.Name))
	return exec
}

// --- Shared formatting ------------------------------------------------------

// fixturesToExecution renders a list of fixtures into compact model text plus
// a football card event for the UI.
func (tc *ToolContext) fixturesToExecution(fixtures []afFixture, cardType, title string) ToolExecution {
	var sb strings.Builder
	sb.WriteString(title + ":\n")
	cards := make([]map[string]interface{}, 0, len(fixtures))
	for _, f := range fixtures {
		sb.WriteString("- " + formatMatchLine(f) + "\n")
		cards = append(cards, fixtureCard(f))
	}
	event := map[string]interface{}{
		"muhiya_football": map[string]interface{}{
			"type":    cardType,
			"title":   title,
			"matches": cards,
		},
	}
	return ToolExecution{LLMContent: strings.TrimSpace(sb.String()), ClientEvent: event}
}

// formatMatchLine produces a single compact line describing a fixture.
func formatMatchLine(f afFixture) string {
	home, away := f.Teams.Home.Name, f.Teams.Away.Name
	status := f.Fixture.Status.Short
	live := isLiveStatus(status)
	finished := status == "FT" || status == "AET" || status == "PEN"

	if (live || finished) && f.Goals.Home != nil && f.Goals.Away != nil {
		score := fmt.Sprintf("%s %d-%d %s", home, *f.Goals.Home, *f.Goals.Away, away)
		if live {
			return fmt.Sprintf("%s: %s (%d', live)", f.League.Name, score, f.Fixture.Status.Elapsed)
		}
		return fmt.Sprintf("%s: %s (full-time)", f.League.Name, score)
	}
	// Scheduled
	when := formatKickoff(f.Fixture.Date)
	return fmt.Sprintf("%s: %s vs %s — %s", f.League.Name, home, away, when)
}

// fixtureCard builds the structured card object for the UI.
func fixtureCard(f afFixture) map[string]interface{} {
	return map[string]interface{}{
		"id":         f.Fixture.ID,
		"league":     f.League.Name,
		"country":    f.League.Country,
		"round":      f.League.Round,
		"status":     f.Fixture.Status.Short,
		"statusLong": f.Fixture.Status.Long,
		"elapsed":    f.Fixture.Status.Elapsed,
		"date":       f.Fixture.Date,
		"venue":      f.Fixture.Venue.Name,
		"home": map[string]interface{}{
			"name":  f.Teams.Home.Name,
			"logo":  f.Teams.Home.Logo,
			"goals": f.Goals.Home,
		},
		"away": map[string]interface{}{
			"name":  f.Teams.Away.Name,
			"logo":  f.Teams.Away.Logo,
			"goals": f.Goals.Away,
		},
	}
}

func isLiveStatus(short string) bool {
	switch short {
	case "1H", "2H", "HT", "ET", "BT", "P", "LIVE", "INT":
		return true
	}
	return false
}

// formatKickoff turns an ISO-8601 fixture date into a friendly UTC string.
func formatKickoff(iso string) string {
	t, err := time.Parse(time.RFC3339, iso)
	if err != nil {
		return iso
	}
	return t.UTC().Format("Mon 02 Jan 15:04") + " UTC"
}

// currentFootballSeason returns the API-Football season start-year. European
// seasons roll over in mid-year, so July onward belongs to the new season.
func currentFootballSeason() int {
	now := time.Now().UTC()
	y := now.Year()
	if now.Month() < time.July {
		return y - 1
	}
	return y
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
