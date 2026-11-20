package tracking

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/net/html"
)

const languageTrendingURL = "https://github.com/trending"
const languageTrendingID = "github-language-daily"
const languageTrendingInterval = 12 * Hour
const githubDailyOffset = Hour // 09:00 Beijing time is 01:00 UTC.

var trendingLanguages = []struct{ name, slug string }{
	{"C++", "c++"}, {"Go", "go"}, {"Java", "java"}, {"JavaScript", "javascript"},
	{"Python", "python"}, {"Rust", "rust"}, {"TypeScript", "typescript"},
	{"C", "c"}, {"C#", "c#"}, {"Kotlin", "kotlin"}, {"PHP", "php"},
	{"Ruby", "ruby"}, {"Scala", "scala"}, {"Shell", "shell"}, {"PowerShell", "powershell"},
	{"Dart", "dart"}, {"Jupyter Notebook", "jupyter-notebook"}, {"HTML", "html"},
	{"CSS", "css"}, {"Swift", "swift"}, {"Vue", "vue"}, {"Markdown", "markdown"},
	{"MDX", "mdx"}, {"Dockerfile", "dockerfile"},
}

type trendingRepository struct {
	Name  string `json:"name"`
	Today int64  `json:"today"`
}

type languageTrendingRound struct {
	Slot            int64                           `json:"slot"`
	Pages           map[string][]trendingRepository `json:"pages"`
	Failures        map[string]string               `json:"failures"`
	Submitted       map[string]bool                 `json:"submitted"`
	TrackAdmissions bool                            `json:"trackAdmissions,omitempty"`
}

func githubDiscoverySlot(now, interval int64) int64 {
	return floorDiv(now-githubDailyOffset, interval)*interval + githubDailyOffset
}

func trendingAttr(n *html.Node, key string) (string, bool) {
	for _, attr := range n.Attr {
		if attr.Key == key {
			return attr.Val, true
		}
	}
	return "", false
}

func trendingClass(n *html.Node, class string) bool {
	value, _ := trendingAttr(n, "class")
	return slices.Contains(strings.Fields(value), class)
}

func findTrendingNode(root *html.Node, match func(*html.Node) bool) *html.Node {
	for n := range root.Descendants() {
		if n.Type == html.ElementNode && match(n) {
			return n
		}
	}
	return nil
}

func trendingText(n *html.Node) string {
	var value strings.Builder
	for child := range n.Descendants() {
		if child.Type == html.TextNode {
			value.WriteString(child.Data)
		}
	}
	return strings.Join(strings.Fields(value.String()), " ")
}

var todayStars = regexp.MustCompile(`^([0-9]+(?:,[0-9]{3})*) stars? today$`)
var invalidTrendingPage = errors.New("GitHub Trending invalid page")

func parseLanguageTrending(body string) ([]trendingRepository, error) {
	root, err := html.Parse(strings.NewReader(body))
	if err != nil {
		return nil, invalidTrendingPage
	}
	results := []trendingRepository{}
	for row := range root.Descendants() {
		if row.Type != html.ElementNode || row.Data != "article" || !trendingClass(row, "Box-row") {
			continue
		}
		heading := findTrendingNode(row, func(n *html.Node) bool { return n.Data == "h2" })
		count := findTrendingNode(row, func(n *html.Node) bool {
			return n.Data == "span" && trendingClass(n, "float-sm-right")
		})
		if heading == nil || count == nil {
			return nil, invalidTrendingPage
		}
		link := findTrendingNode(heading, func(n *html.Node) bool { return n.Data == "a" })
		if link == nil {
			return nil, invalidTrendingPage
		}
		path, _ := trendingAttr(link, "href")
		name := repositoryName("https://github.com"+path, false)
		match := todayStars.FindStringSubmatch(trendingText(count))
		if name == "" || len(match) != 2 {
			return nil, invalidTrendingPage
		}
		stars, err := strconv.ParseInt(strings.ReplaceAll(match[1], ",", ""), 10, 64)
		if err != nil {
			return nil, invalidTrendingPage
		}
		results = append(results, trendingRepository{Name: name, Today: stars})
	}
	if len(results) == 0 && findTrendingNode(root, func(n *html.Node) bool {
		return n.Data == "h2" && trendingClass(n, "blankslate-heading") &&
			strings.Contains(strings.ReplaceAll(trendingText(n), "’", "'"), "don't have any trending repositories")
	}) == nil {
		return nil, invalidTrendingPage
	}
	return results, nil
}

func (round *languageTrendingRound) ranking() []trendingRepository {
	unique := map[string]trendingRepository{}
	for _, page := range round.Pages {
		for _, repo := range page {
			key := strings.ToLower(repo.Name)
			prior, found := unique[key]
			if !found || repo.Today > prior.Today || (repo.Today == prior.Today && repo.Name < prior.Name) {
				unique[key] = repo
			}
		}
	}
	ranked := make([]trendingRepository, 0, len(unique))
	for _, repo := range unique {
		ranked = append(ranked, repo)
	}
	slices.SortFunc(ranked, func(a, b trendingRepository) int {
		return cmp.Or(cmp.Compare(b.Today, a.Today), cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)))
	})
	return ranked
}

// 日榜进度和已提交候选一起持久化，补抓只能提交本轮新进入前 100 的仓库。
func (s *Scheduler) discoverLanguageTrending(ctx context.Context, state *discoveryState, now, deadline int64, previous *sourceState) (sourceState, []string, bool) {
	status := sourceState{}
	if previous != nil {
		status = *previous
	}
	slot := githubDiscoverySlot(now, languageTrendingInterval)
	deadline = min(deadline, slot+languageTrendingInterval)
	round := state.LanguageTrending
	if round == nil || round.Slot != slot {
		round = &languageTrendingRound{
			Slot: slot, Pages: map[string][]trendingRepository{},
			Failures: map[string]string{}, Submitted: map[string]bool{}, TrackAdmissions: true,
		}
		state.LanguageTrending = round
		status.Admitted = nil
	}
	attempted := false
	for _, lang := range trendingLanguages {
		if _, done := round.Pages[lang.slug]; done {
			continue
		}
		raw := languageTrendingURL + "/" + url.PathEscape(lang.slug) + "?since=daily"
		body, err := s.upstream.text(ctx, raw, "GitHub Trending", nil, "", deadline)
		var deferred *deferredRequest
		if errors.As(err, &deferred) {
			break // All language pages share one host and the same deadline.
		}
		attempted = true
		if err == nil {
			var results []trendingRepository
			results, err = parseLanguageTrending(body)
			if err == nil {
				round.Pages[lang.slug] = results
				delete(round.Failures, lang.slug)
				continue
			}
		}
		round.Failures[lang.slug] = safeError(err)
	}
	ranked := round.ranking()
	var names []string
	for _, repo := range ranked[:min(100, len(ranked))] {
		key := strings.ToLower(repo.Name)
		if !round.Submitted[key] {
			names = append(names, repo.Name)
			round.Submitted[key] = true
		}
	}
	if attempted {
		status.AttemptedAt = now
		if round.TrackAdmissions && status.Admitted == nil {
			status.Admitted = ptr(0)
		}
	}
	status.Succeeded = len(ranked)
	status.Failed, status.Error = len(round.Failures), nil
	status.RetryAfter, status.NextAt = 0, slot+languageTrendingInterval
	if len(round.Pages) == len(trendingLanguages) {
		status.SucceededAt = ptr(now)
		return status, names, attempted
	}
	status.RetryAfter = s.upstream.clock.Now() + retryInterval
	status.NextAt = min(status.NextAt, max(status.RetryAfter, s.upstream.blocked(languageTrendingURL)))
	if status.Failed > 0 {
		failures := []string{}
		for _, lang := range trendingLanguages {
			if message, ok := round.Failures[lang.slug]; ok {
				failures = append(failures, fmt.Sprintf("%s：%s", lang.name, message))
			}
		}
		status.Error = strptr(strings.Join(failures, "；"))
	}
	return status, names, attempted
}
