package tracking

import (
	"encoding/json"
	"errors"
	"io/fs"
	"math"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

type Project struct {
	ID          string               `json:"id"`
	Type        string               `json:"type"`
	RepoID      string               `json:"repoId"`
	Name        string               `json:"name"`
	Description *string              `json:"description"`
	Homepage    *string              `json:"homepage"`
	Language    *string              `json:"language"`
	Topics      []string             `json:"topics"`
	Stars       int64                `json:"stars"`
	EnteredAt   int64                `json:"enteredAt"`
	ObservedAt  int64                `json:"observedAt"`
	AttemptedAt int64                `json:"attemptedAt"`
	Error       *string              `json:"error"`
	Stale       bool                 `json:"stale"`
	Trends      map[int]TrendSummary `json:"trends"`
}
type Summary struct {
	Tracked      int    `json:"tracked"`
	Fresh        int    `json:"fresh"`
	Accumulating int    `json:"accumulating"`
	Net24        *int64 `json:"net24"`
	Comparable24 int    `json:"comparable24"`
}
type ListResult struct {
	Items     []Project                  `json:"items"`
	Total     int                        `json:"total"`
	Page      int                        `json:"page"`
	PageSize  int                        `json:"pageSize"`
	Pages     int                        `json:"pages"`
	Languages []string                   `json:"languages"`
	Topics    []string                   `json:"topics"`
	Summary   Summary                    `json:"summary"`
	Status    map[string]json.RawMessage `json:"status"`
	AsOf      int64                      `json:"asOf"`
}
type HistoryResult struct {
	Points     []Point `json:"points"`
	Trend      Trend   `json:"trend"`
	Gaps       []Gap   `json:"gaps"`
	ObservedAt int64   `json:"observedAt"`
	Stale      bool    `json:"stale"`
}
type HTTPOptions struct {
	AllowedOrigin, StaticDir string
	Now                      func() time.Time
	Submissions              SubmissionControl
	TrustedProxies           []netip.Prefix
}
type cacheEntry struct {
	body     []byte
	expires  int64
	revision uint64
}
type handler struct {
	store   *Store
	options HTTPOptions
	mu      sync.Mutex
	cache   map[string]cacheEntry
	limiter submissionLimiter
}

func Handler(store *Store, options HTTPOptions) http.Handler {
	if options.Now == nil {
		options.Now = time.Now
	}
	return &handler{store: store, options: options, cache: map[string]cacheEntry{}}
}

type parameterError string

func (e parameterError) Error() string { return string(e) }

type parameters struct {
	q, language, topic, sort, direction, kind string
	page                                      int
}

var pagePattern = regexp.MustCompile(`^[1-9]\d{0,5}$`)

func listParameters(u *url.URL) (parameters, error) {
	values, err := searchParameters(u.RawQuery)
	if err != nil {
		return parameters{}, parameterError("查询参数无效")
	}
	for key, values := range values {
		if !slices.Contains([]string{"q", "language", "topic", "sort", "direction", "page", "type"}, key) || len(values) != 1 || len(utf16.Encode([]rune(values[0]))) > 200 {
			return parameters{}, parameterError("查询参数无效")
		}
	}
	value := func(key, def string) string {
		if a, ok := values[key]; ok {
			return a[0]
		}
		return def
	}
	p := parameters{q: cases.Lower(language.Und).String(strings.TrimSpace(value("q", ""))), language: value("language", ""), topic: value("topic", ""), sort: value("sort", "24"), direction: value("direction", "desc"), kind: value("type", "github")}
	page := value("page", "1")
	if !slices.Contains([]string{"name", "stars", "24", "72", "168", "720"}, p.sort) || !slices.Contains([]string{"asc", "desc"}, p.direction) || !slices.Contains([]string{"github", "linux.do"}, p.kind) || !pagePattern.MatchString(page) {
		return p, parameterError("排序或页码无效")
	}
	p.page, _ = strconv.Atoi(page)
	return p, nil
}
func jsStringCompare(a, b string) int {
	return slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b)))
}
func EnglishCompare(a, b string) int { return collate.New(language.English).CompareString(a, b) }
func (s *Store) list(p parameters, now int64) (ListResult, error) {
	rows, err := s.ProjectRows("WHERE type=?", p.kind)
	if err != nil {
		return ListResult{}, err
	}
	policy, err := s.Policy()
	if err != nil {
		return ListResult{}, err
	}
	projects, err := s.ProjectViews(rows, now, policy)
	if err != nil {
		return ListResult{}, err
	}
	result := ListResult{Items: []Project{}, Languages: []string{}, Topics: []string{}, PageSize: 100, AsOf: now, Status: map[string]json.RawMessage{}}
	languages, topics := map[string]bool{}, map[string]bool{}
	filtered := []Project{}
	lower := cases.Lower(language.Und)
	net := int64(0)
	for _, project := range projects {
		if project.Language != nil && *project.Language != "" {
			languages[*project.Language] = true
		}
		for _, topic := range project.Topics {
			topics[topic] = true
		}
		result.Summary.Tracked++
		if !project.Stale {
			result.Summary.Fresh++
		}
		if delta := project.Trends[24].Delta; delta != nil {
			net += *delta
			result.Summary.Comparable24++
		} else {
			result.Summary.Accumulating++
		}
		description := ""
		if project.Description != nil {
			description = *project.Description
		}
		if p.q != "" && !strings.Contains(lower.String(project.Name+" "+description), p.q) {
			continue
		}
		if p.language != "" && (project.Language == nil || *project.Language != p.language) {
			continue
		}
		if p.topic != "" && !slices.Contains(project.Topics, p.topic) {
			continue
		}
		filtered = append(filtered, project)
	}
	if result.Summary.Comparable24 > 0 {
		result.Summary.Net24 = &net
	}
	for language := range languages {
		result.Languages = append(result.Languages, language)
	}
	for topic := range topics {
		result.Topics = append(result.Topics, topic)
	}
	slices.SortFunc(result.Languages, jsStringCompare)
	slices.SortFunc(result.Topics, jsStringCompare)
	collator := collate.New(language.English)
	slices.SortStableFunc(filtered, func(a, b Project) int {
		comparison := 0
		if p.sort == "name" {
			comparison = collator.CompareString(lower.String(a.Name), lower.String(b.Name))
		} else {
			av, bv := &a.Stars, &b.Stars
			if p.sort != "stars" {
				width, _ := strconv.Atoi(p.sort)
				av, bv = a.Trends[width].Delta, b.Trends[width].Delta
			}
			if av == nil || bv == nil {
				if av != bv {
					if av == nil {
						return 1
					}
					return -1
				}
				return collator.CompareString(a.Name, b.Name)
			}
			if *av < *bv {
				comparison = -1
			} else if *av > *bv {
				comparison = 1
			}
		}
		if p.direction == "desc" {
			comparison = -comparison
		}
		if comparison == 0 {
			return collator.CompareString(a.Name, b.Name)
		}
		return comparison
	})
	result.Total = len(filtered)
	result.Pages = max(1, (len(filtered)+99)/100)
	result.Page = min(p.page, result.Pages)
	result.Items = filtered[min((result.Page-1)*100, len(filtered)):min(result.Page*100, len(filtered))]
	for _, key := range []string{"discovery", "collection", "admission", "cleanup", "scheduler"} {
		var raw json.RawMessage
		ok, err := s.State(key+":"+p.kind, &raw)
		if err != nil {
			return result, err
		}
		if !ok && (key == "scheduler" || p.kind == "github") {
			ok, err = s.State(key, &raw)
			if err != nil {
				return result, err
			}
		}
		if ok {
			result.Status[key] = raw
		}
	}
	if raw, ok := result.Status["collection"]; ok {
		var legacy runState
		if err := json.Unmarshal(raw, &legacy); err != nil {
			return result, err
		}
		if legacy.Version < 2 {
			// Old batches mixed candidate admission with sampling. Project rows
			// retain the actual update results, even before the next sampling slot.
			sampling := runState{}
			for _, row := range rows {
				sampling.AttemptedAt = max(sampling.AttemptedAt, row.AttemptedAt)
				if sampling.SucceededAt == nil || row.ObservedAt > *sampling.SucceededAt {
					sampling.SucceededAt = ptr(row.ObservedAt)
				}
				if row.Error != nil {
					sampling.Failed++
					sampling.Error = row.Error
				} else {
					sampling.Succeeded++
				}
			}
			result.Status["collection"], _ = json.Marshal(sampling)
			if _, exists := result.Status["admission"]; !exists && legacy.Failed > sampling.Failed {
				var pending []*discoveryEntry
				if _, err := s.State("pending", &pending); err != nil {
					return result, err
				}
				candidates := 0
				for _, entry := range pending {
					if entry != nil && (entry.Type == p.kind || entry.Type == "" && p.kind == "github") && repositoryName("https://github.com/"+entry.Name, false) != "" {
						candidates++
					}
				}
				legacy.Failed = min(legacy.Failed-sampling.Failed, candidates)
				legacy.Succeeded = 0
				legacy.SucceededAt = nil
				if legacy.Failed > 0 {
					result.Status["admission"], _ = json.Marshal(legacy)
				}
			}
		}
	}
	return result, nil
}
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		h.api(w, r)
		return
	}
	h.static(w, r)
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func (h *handler) api(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin != "" && origin != h.options.AllowedOrigin {
		writeJSON(w, 403, map[string]string{"error": "来源不被允许"})
		return
	}
	w.Header().Set("Vary", "Origin")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	if origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}
	fail := func(status int, message string) { writeJSON(w, status, map[string]string{"error": message}) }
	if r.URL.Path == "/api/submissions" || strings.HasPrefix(r.URL.Path, "/api/submissions/") {
		h.submissionAPI(w, r)
		return
	}
	if r.Method != "GET" && r.Method != "OPTIONS" {
		w.Header().Set("Allow", "GET, OPTIONS")
		fail(405, "仅支持只读查询")
		return
	}
	if r.Method == "OPTIONS" {
		w.Header().Set("Access-Control-Allow-Methods", "GET, OPTIONS")
		w.WriteHeader(204)
		return
	}
	now := h.options.Now().UnixMilli()
	var result any
	var err error
	if r.URL.Path == "/api/projects" {
		var p parameters
		p, err = listParameters(r.URL)
		if err == nil {
			key := r.URL.EscapedPath() + "?" + r.URL.RawQuery
			h.mu.Lock()
			entry, ok := h.cache[key]
			h.mu.Unlock()
			revision := h.store.listRevision.Load()
			if ok && entry.expires > now && entry.revision == revision {
				w.Header().Set("Content-Type", "application/json")
				w.Write(entry.body)
				return
			}
			var list ListResult
			list, err = h.store.list(p, now)
			result = list
			if err == nil && list.Summary.Tracked > 0 {
				var body []byte
				body, err = json.Marshal(list)
				if err == nil {
					h.mu.Lock()
					for key, entry := range h.cache {
						if entry.expires <= now {
							delete(h.cache, key)
						}
					}
					if len(h.cache) >= 128 {
						for key := range h.cache {
							delete(h.cache, key)
							break
						}
					}
					h.cache[key] = cacheEntry{body: body, expires: now + 600_000, revision: revision}
					h.mu.Unlock()
				}
			}
		}
	} else {
		path := r.URL.EscapedPath()
		prefix, suffix := "/api/projects/", "/history"
		if !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
			fail(404, "接口不存在")
			return
		}
		idText := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
		if idText == "" || strings.Contains(idText, "/") {
			fail(404, "接口不存在")
			return
		}
		var id string
		id, err = url.PathUnescape(idText)
		if err != nil || !utf8.ValidString(id) {
			err = parameterError("项目标识无效")
		}
		hours := 24
		if err == nil {
			var values url.Values
			values, err = searchParameters(r.URL.RawQuery)
			if err != nil {
				err = parameterError("趋势窗口无效")
			}
			for key, parts := range values {
				if key != "window" || len(parts) != 1 {
					err = parameterError("趋势窗口无效")
				}
			}
			if value, ok := values["window"]; ok {
				n, e := jsNumber(value[0])
				if e != nil || math.Trunc(n) != n {
					err = parameterError("趋势窗口无效")
				} else {
					hours = int(n)
				}
			}
			if !slices.Contains(Windows, hours) {
				err = parameterError("趋势窗口无效")
			}
		}
		if err == nil {
			var rows []ProjectRow
			rows, err = h.store.ProjectRows("WHERE id=?", id)
			if err == nil && len(rows) == 0 {
				fail(404, "项目已淘汰或不存在，请刷新列表")
				return
			}
			if err == nil {
				row := rows[0]
				var histories map[string][]Point
				histories, err = h.store.Histories([]string{row.Cycle}, now)
				if err == nil {
					var policy *SamplingPolicy
					policy, err = h.store.Policy()
					trend := WindowTrend(histories[row.Cycle], hours, now, policy)
					result = HistoryResult{Points: trend.Points, Trend: trend, Gaps: trend.Gaps, ObservedAt: row.ObservedAt, Stale: IsStale(row.ObservedAt, now, policy)}
				}
			}
		}
	}
	if err != nil {
		var parameter parameterError
		if errors.As(err, &parameter) {
			fail(400, string(parameter))
		} else {
			fail(503, "数据暂时不可用，请稍后重试")
		}
		return
	}
	writeJSON(w, 200, result)
}
func jsNumber(text string) (float64, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "0x") || strings.HasPrefix(text, "0X") {
		n, e := strconv.ParseUint(text[2:], 16, 64)
		return float64(n), e
	}
	if strings.HasPrefix(text, "0b") || strings.HasPrefix(text, "0B") {
		n, e := strconv.ParseUint(text[2:], 2, 64)
		return float64(n), e
	}
	if strings.HasPrefix(text, "0o") || strings.HasPrefix(text, "0O") {
		n, e := strconv.ParseUint(text[2:], 8, 64)
		return float64(n), e
	}
	return strconv.ParseFloat(text, 64)
}
func (h *handler) static(w http.ResponseWriter, r *http.Request) {
	if h.options.StaticDir == "" {
		http.NotFound(w, r)
		return
	}
	name := strings.TrimPrefix(filepath.Clean("/"+r.URL.Path), "/")
	if name == "." || name == "" {
		name = "index.html"
	}
	stat, err := os.Stat(filepath.Join(h.options.StaticDir, name))
	if errors.Is(err, fs.ErrNotExist) && !strings.Contains(filepath.Base(name), ".") {
		name = "index.html"
		stat, err = os.Stat(filepath.Join(h.options.StaticDir, name))
	}
	if err != nil || stat.IsDir() {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeFile(w, r, filepath.Join(h.options.StaticDir, name))
}

// URLSearchParams treats semicolons as ordinary text and tolerates malformed
// percent escapes; net/url.ParseQuery uses different parsing rules.
func searchParameters(raw string) (url.Values, error) {
	values := url.Values{}
	decode := func(raw string) string {
		var out strings.Builder
		for i := 0; i < len(raw); i++ {
			if raw[i] == '+' {
				out.WriteByte(' ')
				continue
			}
			if raw[i] == '%' && i+2 < len(raw) {
				if n, err := strconv.ParseUint(raw[i+1:i+3], 16, 8); err == nil {
					out.WriteByte(byte(n))
					i += 2
					continue
				}
			}
			out.WriteByte(raw[i])
		}
		return strings.ToValidUTF8(out.String(), "\uFFFD")
	}
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		key, value, _ := strings.Cut(part, "=")
		values.Add(decode(key), decode(value))
	}
	return values, nil
}
