package tracking

// 本文件从 RSS/Markdown/HelloGithub 页面提取收录候选，尚不直接创建追踪项目。
// parseSource 负责解析，discover 负责调度与状态汇总；候选交给 scheduler.go 做准入和观测。

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"
)

// runState 保存一次发现、准入或观测的状态；Version=2 的 collection 只统计存量项目采样。
type runState struct {
	// collection 状态版本 2 只统计已收录项目的采样，不混入候选准入。
	Version     int     `json:"version,omitempty"`
	AttemptedAt int64   `json:"attemptedAt"`
	SucceededAt *int64  `json:"succeededAt"`
	Succeeded   int     `json:"succeeded"`
	Failed      int     `json:"failed"`
	Error       *string `json:"error"`
}

// sourceState 在通用运行状态上嵌入来源下次执行与重试时间；嵌入字段可直接用 st.Failed 访问。
type sourceState struct {
	runState
	NextAt     int64 `json:"nextAt"`
	RetryAfter int64 `json:"retryAfter,omitempty"`
}

// detailTask HelloGithub 详情页的待解析任务；只有取得真实 GitHub 仓库地址后才成为收录候选。
type detailTask struct {
	URL    string  `json:"url"`
	NextAt int64   `json:"nextAt"`
	Error  *string `json:"error"`
}

// resolvedDetail 缓存当日已解析的详情页地址，避免同一天重复抓取。
type resolvedDetail struct {
	Day  int64  `json:"day"`
	Name string `json:"name"`
}

// discoveryState 保存来源进度、待解析详情和当日缓存；写入 system_state 以支持重启恢复。
type discoveryState struct {
	ScheduleVersion int                       `json:"scheduleVersion,omitempty"`
	Sources         map[string]*sourceState   `json:"sources"`
	Details         []*detailTask             `json:"details"`
	Resolved        map[string]resolvedDetail `json:"resolved"`
}

// discoveryEntry 待收录仓库及其追踪类型；仓库资料与 README 重试分别计时，不让一类失败阻塞另一类。
type discoveryEntry struct {
	Failure      string `json:"failure,omitempty"`
	Type         string `json:"type"`
	Name         string `json:"name"`
	RepoID       string `json:"repoId,omitempty"`
	NextAt       int64  `json:"nextAt,omitempty"`
	ReadmeNextAt int64  `json:"readmeNextAt,omitempty"`
}

// source 定义趋势源身份、解析方式、地址和追踪类型；scheduled 表示跟随采样计划，否则每日发现。
type source struct {
	id, kind, url, typ string
	scheduled          bool
}

// 七个地址都要遍历；直连和代理只是同一趋势源的入口，不是主备成功即停止的策略。
var linuxRSSURLs = []string{
	"https://linux.do/c/develop/4.rss",
	"https://linux.do/tag/2234-tag/2234.rss",
	"https://linux.do/c/domestic/98.rss",
	"https://linux.do/tag/1451-tag/1451.rss",
	"https://rss.yicai-tech.com/rss/from/https://linux.do/c/develop/4.rss",
	"https://rss.yicai-tech.com/rss/from/https://linux.do/tag/2234-tag/2234.rss",
	"https://rss.yicai-tech.com/rss/from/https://linux.do/c/domestic/98.rss",
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
	{"linuxdo", "linuxdo", linuxRSSURLs[0], "linux.do", true},
}
var projectTypes = []string{"github", "linux.do"}

// strptr 返回字符串地址，用于可空文本；用途与 trends.go 的 ptr 相同。
func strptr(s string) *string   { return &s }

// beijingDay 将 Unix 毫秒换成北京时间日序号，只用于分日，返回值不是毫秒时间戳。
func beijingDay(at int64) int64 { return floorDiv(at+8*Hour, Day) }

// 反引号包围原始字符串，反斜杠不经 Go 字符串转义，因此正则可直接写 \. 匹配点号。
// MustCompile 用于固定的源码正则；表达式写错会 panic，普通外部输入应使用可返回错误的 Compile。
var rootRepository = regexp.MustCompile(`(?i)^https://github\.com/([\w.-]+)/([\w.-]+)/?(?:[?#].*)?$`)
var broadRepository = regexp.MustCompile(`(?i)^https?://github\.com/([\w.-]+)/([\w.-]+)(?:[/?#].*)?$`)

// repositoryName 从链接提取 owner/repo 并排除 GitHub 保留路径；broad 允许仓库子路径归一到根仓库。
// 无有效仓库返回空字符串，发现解析可以跳过它；投稿会进一步报参数错误。
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
	if slices.Contains([]string{"trending", "topics", "collections", "sponsors", "features", "marketplace", "settings", "orgs", "users", "search", "user-attachments"}, strings.ToLower(owner)) {
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

// repositoryLinks 提取正文里的仓库链接，忽略大小写去重并保留最新拼写；Markdown 榜单排除发布榜单的组织。
func repositoryLinks(text string, markdown bool) []string {
	names := []string{}
	seen := map[string]int{}
	for _, link := range repoLink.FindAllString(text, -1) {
		name := repositoryName(strings.TrimRight(link, ".,;:!?"), true)
		if name == "" || (markdown && strings.EqualFold(strings.Split(name, "/")[0], "opengithubs")) {
			continue
		}
		key := strings.ToLower(name)
		// map 查询的第二个返回值表示键是否存在；索引 0 本身也有效，不能用 i==0 判断未命中。
		if i, ok := seen[key]; ok {
			names[i] = name
		} else {
			seen[key] = len(names)
			names = append(names, name)
		}
	}
	return names
}

// rssItem 承接 RSS 条目的链接与正文；xml 标签指导标准库解码，encoded 对应 content:encoded 的本地名。
type rssItem struct {
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	Description string `xml:"description"`
	Content     string `xml:"encoded"`
}

// rssItems 严格解析 RSS 根节点、channel 和条目；合法空频道是成功，畸形 XML 或缺频道才报错。
func rssItems(text string) ([]rssItem, error) {
	decoder := xml.NewDecoder(strings.NewReader(text))
	var root xml.StartElement
	for {
		token, e := decoder.Token()
		if e != nil {
			return nil, errors.New("RSS invalid XML")
		}
		// 类型断言检查 XML token 的具体类型，ok=false 时直接跳过，不需要异常处理。
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

// parseHelloProject 从页面 __NEXT_DATA__ 的 JSON 中提取仓库根地址，不靠页面任意链接猜测目标。
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

// parseSource 按来源选择解析策略：Markdown 扫链接，Linux.do 扫条目正文，GitHub RSS 使用权威链接字段。
// HelloGithub RSS 先返回详情页任务地址，再由 parseHelloProject 获取仓库地址。
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

// linuxDiscovery 汇总七个 RSS 的候选、失败与重试信息；complete 表示整组成功，不是“至少一个入口成功”。
type linuxDiscovery struct {
	names     []string
	failure   string
	retryAt   int64
	attempted bool
	complete  bool
}

// linuxRepositories 遍历七个固定 Linux.do RSS 并合并去重；部分失败仍保留成功候选。
// 只有全部成功才刷新整组成功时间；冷却跳过当前主机，预算耗尽停止本轮，重试服从最早可用主机。
func (u *upstream) linuxRepositories(ctx context.Context, deadline int64) linuxDiscovery {
	result := linuxDiscovery{complete: true}
	messages := []string{}
	seen := map[string]bool{}
	for i, raw := range linuxRSSURLs {
		text, err := u.text(ctx, raw, "RSS", nil, "", deadline)
		var names []string
		if err == nil {
			names, err = parseSource(text, "linuxdo")
		}
		var deferred *deferredRequest
		if errors.As(err, &deferred) {
			result.complete = false
			if deferred.retryAt == 0 {
				break
			}
			continue
		}
		result.attempted = true
		if err != nil {
			result.complete = false
			messages = append(messages, fmt.Sprintf("RSS %d: %s", i+1, safeError(err)))
			continue
		}
		for _, name := range names {
			key := strings.ToLower(name)
			if !seen[key] {
				seen[key] = true
				result.names = append(result.names, name)
			}
		}
	}
	result.failure = strings.Join(messages, "; ")
	if !result.complete {
		// 一个主机仍在冷却时，不能推迟另一个已可请求主机的重试。
		ready := u.blocked(linuxRSSURLs[0])
		for _, raw := range linuxRSSURLs[1:] {
			ready = min(ready, u.blocked(raw))
		}
		result.retryAt = max(u.clock.Now()+retryInterval, ready)
	}
	return result
}

// discoveryResult 将本轮新增候选、来源进度和按追踪类型拆分的状态一起交给调度器持久化。
type discoveryResult struct {
	entries []*discoveryEntry
	state   discoveryState
	status  runState
	byType  map[string]runState
	changed bool
}

// discover 恢复来源状态，执行到期趋势源与最多 20 个详情请求，并汇总候选和失败信息。
// 整轮发现共享三分钟预算；升级时重排正常来源时刻，但保留失败退避，不回溯抓取历史帖子。
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
	// 预算只约束本轮发现来源与详情请求；后续仓库准入/采样有自己的单请求超时。
	deadline := u.clock.Now() + 3*60_000
	changed := false
	if policy != nil && now >= policy.EffectiveAt && state.ScheduleVersion != policy.Version {
		for _, source := range sources {
			previous := state.Sources[source.id]
			if !source.scheduled || previous == nil {
				continue
			}
			next := SamplingSlot(now, policy)
			if previous.SucceededAt != nil {
				next = nextExpected(*previous.SucceededAt, policy)
			}
			backoff := int64(0)
			if previous.Failed > 0 {
				backoff = previous.NextAt
			}
			previous.NextAt = max(next, backoff)
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
	seen := map[string]bool{}
	add := func(name, typ string) {
		key := typ + ":" + strings.ToLower(name)
		if !seen[key] {
			seen[key] = true
			entries = append(entries, &discoveryEntry{Type: typ, Name: name})
		}
	}
	attempted, succeeded := false, false
	attemptedTypes, succeededTypes := map[string]bool{}, map[string]bool{}
	for _, source := range sources {
		previous := state.Sources[source.id]
		next := int64(0)
		if previous != nil {
			next = previous.NextAt
			if source.kind == "linuxdo" && previous.Failed > 0 && previous.RetryAfter > 0 {
				// 旧 NextAt 可能只包含直连主机冷却；用独立重试时间避免阻塞可用代理。
				next = min(next, previous.RetryAfter)
			}
		}
		if next > now {
			continue
		}
		var values []string
		var err error
		if source.kind == "linuxdo" {
			result := u.linuxRepositories(ctx, deadline)
			values = result.names
			if !result.complete {
				for _, name := range values {
					add(name, source.typ)
				}
				st := sourceState{}
				if previous != nil {
					st = *previous
				}
				st.NextAt, st.RetryAfter = result.retryAt, result.retryAt
				if result.attempted {
					attempted = true
					attemptedTypes[source.typ] = true
					st.AttemptedAt, st.Succeeded = now, len(values)
				}
				if result.failure != "" {
					st.Failed, st.Error = 1, strptr(result.failure)
				}
				state.Sources[source.id] = &st
				changed = true
				continue
			}
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
			retryAt := max(u.clock.Now()+retryInterval, u.blocked(source.url))
			state.Sources[source.id] = &sourceState{runState: runState{AttemptedAt: now, SucceededAt: successAt, Failed: 1, Error: strptr(message)}, NextAt: retryAt, RetryAfter: u.clock.Now() + retryInterval}
			continue
		}
		attempted = true
		attemptedTypes[source.typ] = true
		if source.kind == "hellogithub" {
			for _, raw := range values {
				if cached, ok := state.Resolved[raw]; ok {
					add(cached.Name, "github")
				} else if !slices.ContainsFunc(state.Details, func(task *detailTask) bool { return task.URL == raw }) {
					state.Details = append(state.Details, &detailTask{URL: raw, NextAt: now})
				}
			}
		} else {
			for _, name := range values {
				add(name, source.typ)
			}
		}
		next = (floorDiv(u.clock.Now()-Hour, Day)+1)*Day + Hour
		if source.scheduled {
			if policy != nil && u.clock.Now() >= policy.EffectiveAt {
				next = nextExpected(u.clock.Now(), policy)
			} else {
				next = (floorDiv(u.clock.Now(), Hour) + 1) * Hour
			}
		}
		state.Sources[source.id] = &sourceState{runState: runState{AttemptedAt: now, SucceededAt: ptr(now), Succeeded: len(values)}, NextAt: next}
		succeeded = true
		succeededTypes[source.typ] = true
	}
	requests := 0
	// 遍历浅拷贝的 slice，再从原 slice 删除已完成任务，避免边遍历边缩短原列表而漏项。
	// 元素仍是 *detailTask，共享任务对象，因此设置 NextAt/Error 会反映到原进度中。
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
			task.NextAt = max(u.clock.Now()+retryInterval, u.blocked(task.URL))
			continue
		}
		attempted = true
		attemptedTypes["github"] = true
		add(name, "github")
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

// priorRun 读取指定业务阶段/追踪类型的上次状态；旧 GitHub 数据无类型键时兼容读取原全局键。
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
