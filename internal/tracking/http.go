package tracking

// 本文件将数据库数据转换成 HTTP 查询结果，并提供前端静态文件。
// 先看 Handler → ServeHTTP → api；全量列表由后台缓存生成，投稿接口在 submissions_http.go。

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
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

// Project 提供给前端的追踪项目视图，与数据库原始行 ProjectRow 分离。
// 大写字段可供 encoding/json 反射访问，反引号中的 json 标签指定外部字段名，类似序列化注解。
// 可空字段用指针，集合用 slice/map；默认 nil 集合编码为 null，显式空 slice 编码为 []。
type Project struct {
	// ID 是追踪项目身份（类型+仓库 ID），RepoID 是 GitHub 稳定仓库身份；同仓库可有两个项目。
	ID          string   `json:"id"`
	Type        string   `json:"type"`
	RepoID      string   `json:"repoId"`
	Name        string   `json:"name"`
	Description *string  `json:"description"`
	Homepage    *string  `json:"homepage"`
	Language    *string  `json:"language"`
	Topics      []string `json:"topics"`
	Stars       int64    `json:"stars"`
	// EnteredAt 决定本周期保护期；ObservedAt 是最后成功观测，AttemptedAt 包含失败尝试。
	// 三者不能互相替代；共享旧观测收录新类型时，入库时间可能晚于观测时间。
	EnteredAt   int64 `json:"enteredAt"`
	ObservedAt  int64 `json:"observedAt"`
	AttemptedAt int64 `json:"attemptedAt"`
	// Error 记录采样失败；Stale 独立按时间判断。Trends 的整数键是窗口小时数，JSON 编码为字符串键。
	Error  *string              `json:"error"`
	Stale  bool                 `json:"stale"`
	Trends map[int]TrendSummary `json:"trends"`
}

// Summary 当前追踪类型的全量统计，先于关键词/语言/标签筛选计算；Net24 为 nil 表示没有可比较项目。
type Summary struct {
	Tracked      int    `json:"tracked"`
	Fresh        int    `json:"fresh"`
	Accumulating int    `json:"accumulating"`
	Net24        *int64 `json:"net24"`
	Comparable24 int    `json:"comparable24"`
}

// ListResult 全量列表响应，包含筛选选项、汇总、运行状态与生成时间。
type ListResult struct {
	Items     []Project                  `json:"items"`
	Total     int                        `json:"total"`
	Languages []string                   `json:"languages"`
	Topics    []string                   `json:"topics"`
	Summary   Summary                    `json:"summary"`
	Status    map[string]json.RawMessage `json:"status"`
	AsOf      int64                      `json:"asOf"`
}

// HistoryResult 历史 API 的 JSON 响应；缺测、覆盖状态和观测过期分别表达不同信息。
type HistoryResult struct {
	Points     []Point `json:"points"`
	Trend      Trend   `json:"trend"`
	Gaps       []Gap   `json:"gaps"`
	ObservedAt int64   `json:"observedAt"`
	Stale      bool    `json:"stale"`
}

// HTTPOptions HTTP 层依赖与配置；Now 函数字段可注入测试时钟，Submissions 只暴露所需控制能力。
type HTTPOptions struct {
	Lists                    *ListCache
	AllowedOrigin, StaticDir string
	Now                      func() time.Time
	Submissions              SubmissionControl
	TrustedProxies           []netip.Prefix
}

// handler serves prepared lists and on-demand details.
type handler struct {
	store   *Store
	options HTTPOptions
	limiter submissionLimiter
}

// Handler 构造并返回 http.Handler 接口，具体对象是 *handler。
// 只要具有 ServeHTTP 方法就自动满足接口，无需 implements，也不需要继承框架基类。
func Handler(store *Store, options HTTPOptions) http.Handler {
	// 函数也是值，可存结构体字段、传参和判 nil；测试注入闭包，生产默认使用 time.Now。
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.Lists == nil {
		options.Lists = NewListCache(store, options.Now)
	}
	return &handler{store: store, options: options}
}

// parameterError 以 string 为底层类型的自定义错误；添加 Error 方法后即可满足 error 接口。
type parameterError string

// Error 返回参数错误文本；类型转换 string(e) 取出底层字符串。
func (e parameterError) Error() string { return string(e) }

// parameters 保存已校验、带默认值的列表参数；小写字段仅供本包使用。
type parameters struct{ kind string }

// listParameters accepts only a tracking type; all user filtering is local.
func listParameters(u *url.URL) (parameters, error) {
	values, err := searchParameters(u.RawQuery)
	if err != nil {
		return parameters{}, parameterError("查询参数无效")
	}
	for key, v := range values {
		if key != "type" || len(v) != 1 {
			return parameters{}, parameterError("查询参数无效")
		}
	}
	kind := values.Get("type")
	if kind == "" {
		kind = "github"
	}
	if kind != "github" && kind != "linux.do" {
		return parameters{}, parameterError("追踪类型无效")
	}
	return parameters{kind: kind}, nil
}

// jsStringCompare 按 UTF-16 码元比较字符串，使选项排序与 JavaScript 行为一致。
func jsStringCompare(a, b string) int {
	return slices.Compare(utf16.Encode([]rune(a)), utf16.Encode([]rune(b)))
}

// EnglishCompare 按英语排序规则比较文本，供淘汰等需要稳定决胜顺序的逻辑使用。
func EnglishCompare(a, b string) int { return collate.New(language.English).CompareString(a, b) }

// list 供后台构造某追踪类型的全量项目、统计、选项和运行状态。
// 旧混合采集状态从项目行还原，避免候选失败污染存量采样状态。
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
	result := ListResult{Items: []Project{}, Languages: []string{}, Topics: []string{}, AsOf: now, Status: map[string]json.RawMessage{}}
	languages, topics := map[string]bool{}, map[string]bool{}
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
		// 先判 nil 再解引用；*delta=0 也算一个可比较项目，不能按“是否非零”决定是否纳入。
		if delta := project.Trends[24].Delta; delta != nil {
			net += *delta
			result.Summary.Comparable24++
		} else {
			result.Summary.Accumulating++
		}
		result.Items = append(result.Items, project)
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
	result.Total = len(result.Items)
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
			// 旧批次混合了候选准入和存量采样；即使下一采样时段未到，
			// 项目行也保留真实更新结果，可据此恢复展示状态。
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

// ServeHTTP 满足 http.Handler 的入口：/api/ 路径交给 API，其余交给静态前端。
// ResponseWriter 是接口，*http.Request 是请求指针；框架会为并发请求调用这个方法。
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/api/") {
		h.api(w, r)
		return
	}
	h.static(w, r)
}

// writeJSON 先设置内容类型和状态码，再编码响应；写出响应后不能再可靠地改状态码。
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

// api 处理来源校验、方法检查、列表缓存和历史查询，并分流投稿接口。
// 内部错误统一映射为安全提示；parameterError 返回 400，其他数据错误返回 503。
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
			entry, ok := h.options.Lists.get(p.kind)
			if !ok {
				fail(503, "列表正在准备，请稍后手动刷新")
				return
			}
			w.Header().Set("Vary", "Origin, Accept-Encoding")
			w.Header().Set("Content-Type", "application/json")
			body := entry.body
			if acceptsGzip(r.Header.Get("Accept-Encoding")) {
				w.Header().Set("Content-Encoding", "gzip")
				body = entry.gzip
			}
			w.Write(body)
			return
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
					trend := WindowTrend(projectHistory(row, histories[row.Cycle]), hours, now, policy)
					result = HistoryResult{Points: trend.Points, Trend: trend, Gaps: trend.Gaps, ObservedAt: row.ObservedAt, Stale: IsStale(row.ObservedAt, now, policy)}
				}
			}
		}
	}
	if err != nil {
		var parameter parameterError
		// errors.As 按错误类型匹配（也可沿包装链查找）；errors.Is 则判断某个已知错误值。
		// 这是 Go 的显式错误分类，不会像异常捕获那样改变程序控制流。
		if errors.As(err, &parameter) {
			fail(400, string(parameter))
		} else {
			fail(503, "数据暂时不可用，请稍后重试")
		}
		return
	}
	writeJSON(w, 200, result)
}

// jsNumber 兼容历史前端的数字参数格式，支持十六/二进制与八进制前缀；调用方继续检查整数及允许窗口。
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

// static 从构建目录提供静态文件；不存在且无扩展名的页面路径回退到 index.html，支持前端路由。
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

// searchParameters 按 URLSearchParams 语义解析查询：分号是普通字符，畸形百分号转义按原文保留。
// 标准 net/url.ParseQuery 的规则不同，因此这里手动解码；无效 UTF-8 替换为 U+FFFD。
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
