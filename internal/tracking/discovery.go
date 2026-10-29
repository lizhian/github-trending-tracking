package tracking

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"io"
	"regexp"
	"slices"
	"strings"
)

type runState struct {
	AttemptedAt int64   `json:"attemptedAt"`
	SucceededAt *int64  `json:"succeededAt"`
	Succeeded   int     `json:"succeeded"`
	Failed      int     `json:"failed"`
	Error       *string `json:"error"`
}
type sourceState struct {
	runState
	NextAt     int64 `json:"nextAt"`
	RetryAfter int64 `json:"retryAfter,omitempty"`
}
type detailTask struct {
	URL      string  `json:"url"`
	ListedAt int64   `json:"listedAt"`
	NextAt   int64   `json:"nextAt"`
	Error    *string `json:"error"`
}
type resolvedDetail struct {
	Day  int64  `json:"day"`
	Name string `json:"name"`
}
type discoveryState struct {
	ScheduleVersion int                       `json:"scheduleVersion,omitempty"`
	Sources         map[string]*sourceState   `json:"sources"`
	Details         []*detailTask             `json:"details"`
	Resolved        map[string]resolvedDetail `json:"resolved"`
}
type discoveryEntry struct {
	Type         string `json:"type"`
	Name         string `json:"name"`
	ListedAt     int64  `json:"listedAt"`
	RepoID       string `json:"repoId,omitempty"`
	NextAt       int64  `json:"nextAt,omitempty"`
	ReadmeNextAt int64  `json:"readmeNextAt,omitempty"`
}
type source struct {
	id, kind, url, typ string
	scheduled          bool
}

var sources = []source{
	{"github-daily", "github", "https://rsshub-balancer.virworks.moe/github/trending/daily/any", "github", true},
	{"github-weekly", "github", "https://rsshub-balancer.virworks.moe/github/trending/weekly/any", "github", false},
	{"github-monthly", "github", "https://rsshub-balancer.virworks.moe/github/trending/monthly/any", "github", false},
	{"hellogithub-all", "hellogithub", "https://rsshub-balancer.virworks.moe/hellogithub/home/all", "github", false},
	{"hellogithub-featured", "hellogithub", "https://rsshub-balancer.virworks.moe/hellogithub/home/featured", "github", false},
	{"opengithubs-daily", "markdown", "https://raw.githubusercontent.com/OpenGithubs/github-daily-rank/refs/heads/main/README.md", "github", false},
	{"opengithubs-weekly", "markdown", "https://raw.githubusercontent.com/OpenGithubs/github-weekly-rank/refs/heads/main/README.md", "github", false},
	{"opengithubs-monthly", "markdown", "https://raw.githubusercontent.com/OpenGithubs/github-monthly-rank/refs/heads/main/README.md", "github", false},
	{"linuxdo", "linuxdo", "https://linux.do/tag/2234-tag/2234.rss", "linux.do", true},
}
var projectTypes = []string{"github", "linux.do"}

func strptr(s string) *string   { return &s }
func beijingDay(at int64) int64 { return floorDiv(at+8*Hour, Day) }

var rootRepository = regexp.MustCompile(`(?i)^https://github\.com/([\w.-]+)/([\w.-]+)/?(?:[?#].*)?$`)
var broadRepository = regexp.MustCompile(`(?i)^https?://github\.com/([\w.-]+)/([\w.-]+)(?:[/?#].*)?$`)

func repositoryName(v string, broad bool) string {
	pattern := rootRepository
	if broad {
		pattern = broadRepository
	}
	m := pattern.FindStringSubmatch(strings.TrimSpace(v))
	if m == nil {
		return ""
	}
	owner, repo := m[1], m[2]
	if slices.Contains([]string{"trending", "topics", "collections", "sponsors", "features", "marketplace", "settings", "orgs", "users", "search"}, strings.ToLower(owner)) {
		return ""
	}
	if strings.HasSuffix(strings.ToLower(repo), ".git") {
		repo = repo[:len(repo)-4]
	}
	if repo == "" || repo == "." || repo == ".." || owner == "." || owner == ".." {
		return ""
	}
	return owner + "/" + repo
}

var repoLink = regexp.MustCompile(`(?i)\bhttps?://github\.com/([\w.-]+)/([\w.-]+)(?:/[^\s<>"'\[\]()，。；：！？]*)?`)

func repositoryLinks(text string, markdown bool) []string {
	names := []string{}
	seen := map[string]int{}
	for _, link := range repoLink.FindAllString(text, -1) {
		name := repositoryName(strings.TrimRight(link, ".,;:!?"), true)
		if name == "" || (markdown && strings.EqualFold(strings.Split(name, "/")[0], "opengithubs")) {
			continue
		}
		key := strings.ToLower(name)
		if i, ok := seen[key]; ok {
			names[i] = name
		} else {
			seen[key] = len(names)
			names = append(names, name)
		}
	}
	return names
}

type rssItem struct {
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	Description string `xml:"description"`
	Content     string `xml:"encoded"`
}

func rssItems(text string) ([]rssItem, error) {
	decoder := xml.NewDecoder(strings.NewReader(text))
	var root xml.StartElement
	for {
		token, e := decoder.Token()
		if e != nil {
			return nil, errors.New("RSS invalid XML")
		}
		if text, ok := token.(xml.CharData); ok && strings.TrimSpace(string(text)) != "" {
			return nil, errors.New("RSS invalid XML")
		}
		if start, ok := token.(xml.StartElement); ok {
			root = start
			break
		}
	}
	if root.Name.Local != "rss" {
		return nil, errors.New("RSS missing channel")
	}
	var doc struct {
		Channel *struct {
			Items []rssItem `xml:"item"`
		} `xml:"channel"`
	}
	if e := decoder.DecodeElement(&doc, &root); e != nil {
		return nil, errors.New("RSS invalid XML")
	}
	for {
		token, e := decoder.Token()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, errors.New("RSS invalid XML")
		}
		if _, ok := token.(xml.StartElement); ok {
			return nil, errors.New("RSS invalid XML")
		}
		if text, ok := token.(xml.CharData); ok && strings.TrimSpace(string(text)) != "" {
			return nil, errors.New("RSS invalid XML")
		}
	}
	if doc.Channel == nil {
		return nil, errors.New("RSS missing channel")
	}
	return doc.Channel.Items, nil
}

var helloURL = regexp.MustCompile(`(?i)^https://hellogithub\.com/repository/[a-f\d]+/?(?:[?#].*)?$`)
var nextScript = regexp.MustCompile(`(?is)<script\b[^>]*\bid=["']__NEXT_DATA__["'][^>]*>(.*?)</script\s*>`)

func parseHelloProject(text string) (string, error) {
	m := nextScript.FindStringSubmatch(text)
	if m != nil {
		var data struct {
			Props struct {
				PageProps struct {
					Repo struct {
						URL string `json:"url"`
					} `json:"repo"`
				} `json:"pageProps"`
			} `json:"props"`
		}
		if json.Unmarshal([]byte(m[1]), &data) == nil {
			if name := repositoryName(data.Props.PageProps.Repo.URL, false); name != "" {
				return name, nil
			}
		}
	}
	return "", errors.New("HelloGithub invalid project")
}
func parseSource(text, kind string) ([]string, error) {
	if kind == "markdown" {
		return repositoryLinks(text, true), nil
	}
	items, e := rssItems(text)
	if e != nil {
		return nil, e
	}
	values := []string{}
	seen := map[string]bool{}
	for _, item := range items {
		if kind == "linuxdo" {
			for _, field := range []string{item.Description, item.Content} {
				for _, name := range repositoryLinks(field, false) {
					key := strings.ToLower(name)
					if !seen[key] {
						seen[key] = true
						values = append(values, name)
					}
				}
			}
			continue
		}
		for _, link := range []string{item.Link, item.GUID} {
			link = strings.TrimSpace(link)
			if kind == "github" {
				if name := repositoryName(link, false); name != "" {
					values = append(values, name)
					break
				}
			} else if helloURL.MatchString(link) {
				link = strings.SplitN(strings.SplitN(link, "?", 2)[0], "#", 2)[0]
				values = append(values, strings.TrimSuffix(link, "/"))
				break
			}
		}
	}
	return values, nil
}

type linuxFailure struct {
	message string
	retryAt int64
}

func (e *linuxFailure) Error() string { return e.message }
func (u *upstream) linuxRepositories(ctx context.Context, primary, fallback string, deadline int64) ([]string, error) {
	if fallback == "" {
		text, e := u.text(ctx, primary, "RSS", nil, "", deadline)
		if e != nil {
			return nil, e
		}
		return parseSource(text, "linuxdo")
	}
	messages := []string{}
	ready := []int64{}
	attempted := false
	for i, raw := range []string{primary, fallback} {
		text, e := u.text(ctx, raw, "RSS", nil, "", deadline)
		if e == nil {
			var names []string
			names, e = parseSource(text, "linuxdo")
			if e == nil {
				return names, nil
			}
		}
		var deferred *deferredRequest
		if errors.As(e, &deferred) {
			if deferred.retryAt == 0 {
				return nil, e
			}
			ready = append(ready, deferred.retryAt)
		} else {
			attempted = true
			ready = append(ready, u.blocked(raw))
		}
		role := "primary"
		if i == 1 {
			role = "fallback"
		}
		messages = append(messages, role+": "+safeError(e))
	}
	earliest := slices.Min(ready)
	if !attempted {
		return nil, &deferredRequest{earliest}
	}
	return nil, &linuxFailure{strings.Join(messages, "; "), max(u.clock.Now()+30*60_000, earliest)}
}

type discoveryResult struct {
	entries []*discoveryEntry
	state   discoveryState
	status  runState
	byType  map[string]runState
	changed bool
}

func (s *Scheduler) discover(ctx context.Context, now int64, policy *SamplingPolicy) (discoveryResult, error) {
	var state discoveryState
	if _, e := s.store.State("discoverySources", &state); e != nil {
		return discoveryResult{}, e
	}
	if state.Sources == nil {
		state.Sources = map[string]*sourceState{}
	}
	if state.Details == nil {
		state.Details = []*detailTask{}
	}
	if state.Resolved == nil {
		state.Resolved = map[string]resolvedDetail{}
	}
	prior, e := s.priorRun("discovery", "")
	if e != nil {
		return discoveryResult{}, e
	}
	priorTypes := map[string]runState{}
	for _, typ := range projectTypes {
		v, e := s.priorRun("discovery", typ)
		if e != nil {
			return discoveryResult{}, e
		}
		priorTypes[typ] = v
	}
	u := s.upstream
	deadline := u.clock.Now() + 3*60_000
	changed := false
	if policy != nil && now >= policy.EffectiveAt && state.ScheduleVersion != policy.Version {
		if daily := state.Sources["github-daily"]; daily != nil {
			next := LatestSlot(now)
			if daily.SucceededAt != nil {
				next = NextSlot(*daily.SucceededAt)
			}
			backoff := int64(0)
			if daily.Failed > 0 {
				backoff = daily.NextAt
			}
			daily.NextAt = max(next, backoff, u.blocked(sources[0].url))
		}
		state.ScheduleVersion = policy.Version
		changed = true
	}
	for key, v := range state.Resolved {
		if v.Day != beijingDay(now) {
			delete(state.Resolved, key)
		}
	}
	entries := []*discoveryEntry{}
	indices := map[string]int{}
	add := func(name string, at int64, typ string) {
		key := typ + ":" + strings.ToLower(name)
		if i, ok := indices[key]; ok {
			entries[i].ListedAt = max(entries[i].ListedAt, at)
		} else {
			indices[key] = len(entries)
			entries = append(entries, &discoveryEntry{Type: typ, Name: name, ListedAt: at})
		}
	}
	attempted, succeeded := false, false
	attemptedTypes, succeededTypes := map[string]bool{}, map[string]bool{}
	fallback := strings.TrimSpace(s.options.FallbackURL)
	for _, source := range sources {
		previous := state.Sources[source.id]
		next := int64(0)
		if previous != nil {
			next = previous.NextAt
			if source.kind == "linuxdo" && fallback != "" && previous.Failed > 0 {
				retry := previous.RetryAfter
				if retry == 0 {
					retry = previous.AttemptedAt + 3*60_000 + 30*60_000
				}
				next = min(next, retry)
			}
		}
		if next > now {
			continue
		}
		var values []string
		var err error
		if source.kind == "linuxdo" {
			values, err = u.linuxRepositories(ctx, source.url, fallback, deadline)
		} else {
			label := "RSS"
			if source.kind == "markdown" {
				label = "OpenGithubs"
			}
			var text string
			text, err = u.text(ctx, source.url, label, nil, "", deadline)
			if err == nil {
				values, err = parseSource(text, source.kind)
			}
		}
		if err != nil {
			var deferred *deferredRequest
			if errors.As(err, &deferred) {
				if deferred.retryAt == 0 {
					break
				}
				continue
			}
			attempted = true
			attemptedTypes[source.typ] = true
			successAt := (*int64)(nil)
			if previous != nil {
				successAt = previous.SucceededAt
			}
			message := safeError(err)
			retryAt := max(u.clock.Now()+30*60_000, u.blocked(source.url))
			var linux *linuxFailure
			if errors.As(err, &linux) {
				message = linux.message
				retryAt = linux.retryAt
			}
			state.Sources[source.id] = &sourceState{runState: runState{AttemptedAt: now, SucceededAt: successAt, Failed: 1, Error: strptr(message)}, NextAt: retryAt, RetryAfter: u.clock.Now() + 30*60_000}
			continue
		}
		attempted = true
		attemptedTypes[source.typ] = true
		if source.kind == "hellogithub" {
			for _, raw := range values {
				if cached, ok := state.Resolved[raw]; ok {
					add(cached.Name, now, "github")
				} else if !slices.ContainsFunc(state.Details, func(task *detailTask) bool { return task.URL == raw }) {
					state.Details = append(state.Details, &detailTask{URL: raw, ListedAt: now, NextAt: now})
				}
			}
		} else {
			for _, name := range values {
				add(name, now, source.typ)
			}
		}
		next = (floorDiv(u.clock.Now()-Hour, Day)+1)*Day + Hour
		if source.scheduled {
			if policy != nil && u.clock.Now() >= policy.EffectiveAt {
				next = NextSlot(u.clock.Now())
			} else {
				next = (floorDiv(u.clock.Now(), Hour) + 1) * Hour
			}
		}
		state.Sources[source.id] = &sourceState{runState: runState{AttemptedAt: now, SucceededAt: ptr(now), Succeeded: len(values)}, NextAt: next}
		succeeded = true
		succeededTypes[source.typ] = true
	}
	requests := 0
	for _, task := range slices.Clone(state.Details) {
		if requests >= 20 {
			break
		}
		if task.NextAt > now {
			continue
		}
		requests++
		text, err := u.text(ctx, task.URL, "HelloGithub", nil, "", deadline)
		name := ""
		if err == nil {
			name, err = parseHelloProject(text)
		}
		if err != nil {
			var deferred *deferredRequest
			if errors.As(err, &deferred) {
				requests--
				if deferred.retryAt == 0 {
					break
				}
				continue
			}
			attempted = true
			attemptedTypes["github"] = true
			task.Error = strptr(safeError(err))
			task.NextAt = max(u.clock.Now()+30*60_000, u.blocked(task.URL))
			continue
		}
		attempted = true
		attemptedTypes["github"] = true
		add(name, task.ListedAt, "github")
		state.Resolved[task.URL] = resolvedDetail{beijingDay(u.clock.Now()), name}
		state.Details = slices.DeleteFunc(state.Details, func(v *detailTask) bool { return v == task })
		succeeded = true
		succeededTypes["github"] = true
	}
	byType := map[string]runState{}
	allErrors := []string{}
	for _, typ := range projectTypes {
		errorsForType := []string{}
		at := int64(0)
		successAt := int64(0)
		for _, source := range sources {
			if source.typ != typ {
				continue
			}
			if st := state.Sources[source.id]; st != nil {
				at = max(at, st.AttemptedAt)
				if st.SucceededAt != nil {
					successAt = max(successAt, *st.SucceededAt)
				}
				if st.Failed > 0 && st.Error != nil {
					errorsForType = append(errorsForType, source.id+": "+*st.Error)
				}
			}
		}
		if typ == "github" {
			for _, task := range state.Details {
				if task.Error != nil {
					errorsForType = append(errorsForType, "hellogithub-detail: "+*task.Error)
				}
			}
		}
		allErrors = append(allErrors, errorsForType...)
		v := priorTypes[typ]
		if v.AttemptedAt == 0 {
			v.AttemptedAt = at
		}
		if v.SucceededAt == nil && successAt > 0 {
			v.SucceededAt = ptr(successAt)
		}
		if attemptedTypes[typ] {
			v.AttemptedAt = now
			v.Succeeded = 0
			for _, entry := range entries {
				if entry.Type == typ {
					v.Succeeded++
				}
			}
		}
		if succeededTypes[typ] {
			v.SucceededAt = ptr(now)
		}
		v.Failed = len(errorsForType)
		v.Error = nil
		if len(errorsForType) > 0 {
			v.Error = strptr(errorsForType[0])
		}
		byType[typ] = v
	}
	status := prior
	if status.AttemptedAt == 0 || attempted {
		status.AttemptedAt = now
	}
	if attempted {
		status.Succeeded = len(entries)
	}
	if succeeded {
		status.SucceededAt = ptr(now)
	}
	status.Failed = len(allErrors)
	status.Error = nil
	if len(allErrors) > 0 {
		status.Error = strptr(allErrors[0])
	}
	return discoveryResult{entries, state, status, byType, changed || attempted}, nil
}
func (s *Scheduler) priorRun(kind, typ string) (runState, error) {
	var result runState
	key := kind
	if typ != "" {
		key += ":" + typ
	}
	found, e := s.store.State(key, &result)
	if e != nil {
		return result, e
	}
	if !found && typ == "github" {
		_, e = s.store.State(kind, &result)
	}
	return result, e
}
