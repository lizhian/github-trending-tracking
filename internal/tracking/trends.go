package tracking

// 本文件集中定义时间单位、历史采样规则和趋势计算。
// 先读 Point/TrendSummary，再读 NextSlot、WindowTrend；ProjectViews 将数据库行转换为页面数据。

import (
	"encoding/json"
	"fmt"
	"math"
)

// 本包统一用 Unix 毫秒算业务时间；time.Duration 则以纳秒为底层单位，二者转换必须乘单位。
// 数字中的下划线仅增加可读性，例如 3_600_000 与 3600000 值相同。
const Hour int64 = 3_600_000
const Day int64 = 24 * Hour
const Retention int64 = 30 * Day
const SamplingGrace int64 = 10 * 60_000

// 趋势宽度以小时为单位；与毫秒常量 Hour 相乘后才是时间跨度。
var Windows = []int{24, 72, 168, 720}
// 当前北京时间计划：09–21 每小时，夜间每两小时，共十八轮；legacy 仅用于解释旧历史。
var SamplingHours = []int64{1, 3, 5, 7, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 23}
var legacySamplingHours = []int64{1, 5, 9, 11, 13, 15, 17, 19, 21}

// SamplingPolicy 记录历史采样规则的两个生效边界，避免用新时刻表误判旧历史缺测。
// omitempty 仅控制 JSON 编码时省略零值，不会改变字段本身的 Go 零值。
type SamplingPolicy struct {
	Version         int   `json:"version"`
	EffectiveAt     int64 `json:"effectiveAt"`
	HourlyDaytimeAt int64 `json:"hourlyDaytimeAt,omitempty"`
}

// Point 一个 Star 快照：[Unix 毫秒时间, Star 总数]。
// [2]int64 是固定长度数组，长度属于类型，赋值复制两个元素；[]Point 则是引用底层数组的 slice。
type Point [2]int64

// Gap 缺测区间：[前一个实际观测时间, 后一个实际观测时间]，并非计划内的正常采样间隔。
type Gap [2]int64

// TrendSummary 一个趋势窗口的净增量、覆盖状态和缺测区间。
// Delta 为 nil 表示尚不可比较，指向 0 则是可比较但净增量为零，不能混为一谈。
type TrendSummary struct {
	Delta *int64 `json:"delta"`
	State string `json:"state"`
	Start *int64 `json:"start"`
	End   *int64 `json:"end"`
	Gaps  []Gap  `json:"gaps"`
}

// Trend 在趋势摘要上附加窗口快照。匿名嵌入使摘要字段可被提升访问，JSON 也会展开。
// 这属于组合，不是 Java 继承；Trend 不会因此自动成为 TrendSummary 的子类型。
type Trend struct {
	TrendSummary
	Points []Point `json:"points"`
}

// ptr 返回数值的地址，用指针表达“有值”。Go 允许返回局部变量地址，编译器保证其存活期，
// 不需要像手动内存管理语言那样担心这里返回悬空栈指针。
func ptr(n int64) *int64        { return &n }

// floorDiv 向负无穷取整的除法，用于时间桶；Go 整数除法向零截断，负时间时语义不同。
func floorDiv(a, b int64) int64 { return int64(math.Floor(float64(a) / float64(b))) }

// LatestSlot 返回当前时刻表中不晚于 at 的最近采样时段，单位为 Unix 毫秒。
func LatestSlot(at int64) int64 {
	return latestSlot(at, SamplingHours)
}

// latestSlot 按给定升序时刻表从后向前查找最近时段；当天尚未开始则退到前一天末次时段。
func latestSlot(at int64, hours []int64) int64 {
	// 先平移到 UTC+8 按天截断，再移回 UTC 毫秒；计算不依赖宿主机器的本地时区。
	day := floorDiv(at+8*Hour, Day)*Day - 8*Hour
	for i := len(hours) - 1; i >= 0; i-- {
		slot := day + hours[i]*Hour
		if slot <= at {
			return slot
		}
	}
	return day - Day + hours[len(hours)-1]*Hour
}

// NextSlot 返回严格晚于 at 的下一采样时段；恰逢整点启动也等待下一轮，不补跑当前轮。
func NextSlot(at int64) int64 {
	return nextSlot(at, SamplingHours)
}

// nextSlot 按给定升序时刻表找下一个时段，跨日时转到明天的首个时段。
func nextSlot(at int64, hours []int64) int64 {
	day := floorDiv(at+8*Hour, Day)*Day - 8*Hour
	for _, hour := range hours {
		slot := day + hour*Hour
		if slot > at {
			return slot
		}
	}
	return day + Day + hours[0]*Hour
}

// SamplingSlot 将观测时间映射为其采样轮次；旧规则生效前仍按整小时归桶。
func SamplingSlot(at int64, p *SamplingPolicy) int64 {
	if p == nil || at < p.EffectiveAt {
		return floorDiv(at, Hour) * Hour
	}
	return observationSlot(at, p)
}

// observationSlot 依据两个生效边界选择历史规则：早期逐小时、旧九时段、当前十八时段。
// 边界前的递归回退避免将尚未生效的新时段当作真实应采时段。
func observationSlot(at int64, p *SamplingPolicy) int64 {
	if p == nil || at < p.EffectiveAt {
		return floorDiv(at, Hour) * Hour
	}
	if p.HourlyDaytimeAt > 0 && at >= p.HourlyDaytimeAt {
		slot := LatestSlot(at)
		if slot >= p.HourlyDaytimeAt {
			return slot
		}
		return observationSlot(p.HourlyDaytimeAt-1, p)
	}
	slot := latestSlot(at, legacySamplingHours)
	if slot >= p.EffectiveAt {
		return slot
	}
	return floorDiv(p.EffectiveAt-1, Hour) * Hour
}

// nextExpected 按当时有效的采样规则推算下一应采时段，并处理规则切换边界。
func nextExpected(at int64, p *SamplingPolicy) int64 {
	hourly := (floorDiv(at, Hour) + 1) * Hour
	if p == nil || hourly < p.EffectiveAt {
		return hourly
	}
	if p.HourlyDaytimeAt > 0 && at >= p.HourlyDaytimeAt {
		return NextSlot(at)
	}
	next := nextSlot(max(at, p.EffectiveAt-1), legacySamplingHours)
	if p.HourlyDaytimeAt > 0 && next >= p.HourlyDaytimeAt {
		return NextSlot(p.HourlyDaytimeAt - 1)
	}
	return next
}

// MissedSampling 判断两个观测之间是否跳过至少一个应采时段；正常计划间隔不算缺测。
func MissedSampling(from, to int64, p *SamplingPolicy) bool {
	return nextExpected(observationSlot(from, p), p) < observationSlot(to, p)
}

// IsStale 根据历史规则及当前时间判断观测是否过期；新规则允许下一应采时段后 10 分钟宽限。
// 数据过期与请求报错、窗口是否完整是互相独立的判断。
func IsStale(observedAt, now int64, p *SamplingPolicy) bool {
	if p == nil || now < p.EffectiveAt {
		return now-observedAt > 2*Hour
	}
	if observedAt+2*Hour < p.EffectiveAt {
		return true
	}
	return now > nextExpected(max(observedAt, p.EffectiveAt-1), p)+SamplingGrace
}

// CompleteWindow 判断首个快照是否到达窗口左边界允许范围；考虑当时采样间隔与宽限。
// 完整观测仅指覆盖范围足够，并不保证窗口内部没有缺测。
func CompleteWindow(start, end int64, hours int, p *SamplingPolicy) bool {
	boundary := end - int64(hours)*Hour
	latestStart := boundary + Hour
	if p != nil && boundary >= p.EffectiveAt {
		latestStart = nextExpected(boundary-1, p) + SamplingGrace
	}
	return start <= latestStart
}

// WindowTrend 从升序快照中过滤最近 30 天有效点，以最新成功观测为窗口终点计算趋势。
// 不足两个不同时间点时为积累中；否则计算可为负的 Star 净增量，并记录跳过的应采时段。
func WindowTrend(points []Point, hours int, now int64, p *SamplingPolicy) Trend {
	valid := []Point{}
	for _, point := range points {
		if point[0] >= now-Retention && point[0] <= now {
			valid = append(valid, point)
		}
	}
	selected := []Point{}
	if len(valid) > 0 {
		end := valid[len(valid)-1][0]
		for _, point := range valid {
			if point[0] >= end-int64(hours)*Hour {
				selected = append(selected, point)
			}
		}
	}
	// 显式空 slice 让 JSON 输出 [] 而非 null。只有至少两个不同观测时间才设置 Delta。
	summary := TrendSummary{State: "accumulating", Gaps: []Gap{}}
	if len(selected) > 0 {
		first, last := selected[0], selected[len(selected)-1]
		summary.Start = ptr(first[0])
		summary.End = ptr(last[0])
		if last[0] > first[0] {
			summary.Delta = ptr(last[1] - first[1])
			summary.State = "partial"
			if CompleteWindow(first[0], last[0], hours, p) {
				summary.State = "complete"
			}
		}
	}
	for i := 1; i < len(selected); i++ {
		if MissedSampling(selected[i-1][0], selected[i][0], p) {
			summary.Gaps = append(summary.Gaps, Gap{selected[i-1][0], selected[i][0]})
		}
	}
	return Trend{TrendSummary: summary, Points: selected}
}

// ProjectViews 将持久化行转换为 API 项目，保留有效摘要，仅对基准已超出 30 天范围的窗口重算。
// 这里读取历史，不改写数据库；同时按当前时间计算过期状态，避免陈旧摘要伪装成完整观测。
func (s *Store) ProjectViews(rows []ProjectRow, now int64, policy *SamplingPolicy) ([]Project, error) {
	// 预留长度上限可减少 append 扩容；当前 len=0，不能直接 items[0] 赋值。
	items := make([]Project, 0, len(rows))
	expired := []string{}
	// map/slice 的零值都是 nil：nil slice 可遍历和 append，nil map 可查询但不能写入。
	// 下面 json.Unmarshal 解码 JSON 对象时会初始化 map；若手工新增键，须先用 make 或 map 字面量初始化。
	for _, row := range rows {
		var trends map[int]TrendSummary
		if err := json.Unmarshal([]byte(row.Trends), &trends); err != nil {
			return nil, err
		}
		needsHistory := false
		for _, width := range Windows {
			trend, ok := trends[width]
			if !ok {
				return nil, fmt.Errorf("project %s is missing a trend window", row.ID)
			}
			if trend.Gaps == nil {
				trend.Gaps = []Gap{}
			}
			if trend.Delta == nil {
				trend.State = "accumulating"
			} else if trend.Start == nil || trend.End == nil {
				return nil, fmt.Errorf("project %s has an invalid trend", row.ID)
			} else if CompleteWindow(*trend.Start, *trend.End, width, policy) {
				trend.State = "complete"
			} else {
				trend.State = "partial"
			}
			if trend.Start != nil && *trend.Start < now-Retention {
				needsHistory = true
			}
			// 从 map 取出的结构体 trend 是副本；修改后要放回 map，才能保留字段更新。
			trends[width] = trend
		}
		var topics []string
		if err := json.Unmarshal([]byte(row.Topics), &topics); err != nil {
			return nil, err
		}
		if topics == nil {
			topics = []string{}
		}
		items = append(items, Project{ID: row.ID, Type: row.Type, RepoID: row.RepoID, Name: row.Name, Description: row.Description, Homepage: row.Homepage, Language: row.Language, Topics: topics, Stars: row.Stars, EnteredAt: row.EnteredAt, ObservedAt: row.ObservedAt, AttemptedAt: row.AttemptedAt, Error: row.Error, Stale: IsStale(row.ObservedAt, now, policy), Trends: trends})
		if needsHistory {
			expired = append(expired, row.Cycle)
		}
	}
	histories, err := s.Histories(expired, now)
	if err != nil {
		return nil, err
	}
	for i, item := range items {
		for _, width := range Windows {
			trend := item.Trends[width]
			if trend.Start == nil || *trend.Start >= now-Retention {
				continue
			}
			updated := TrendSummary{State: "accumulating", Gaps: []Gap{}}
			if item.ObservedAt >= now-Retention {
				first := Point{item.ObservedAt, item.Stars}
				for _, point := range histories[rows[i].Cycle] {
					if point[0] >= max(now-Retention, item.ObservedAt-int64(width)*Hour) && point[0] <= item.ObservedAt {
						first = point
						break
					}
				}
				updated.Start = ptr(first[0])
				updated.End = ptr(item.ObservedAt)
				if item.ObservedAt > first[0] {
					updated.Delta = ptr(item.Stars - first[1])
					updated.State = "partial"
					if CompleteWindow(first[0], item.ObservedAt, width, policy) {
						updated.State = "complete"
					}
				}
				for _, gap := range item.Trends[720].Gaps {
					if gap[0] >= first[0] && gap[1] <= item.ObservedAt {
						updated.Gaps = append(updated.Gaps, gap)
					}
				}
			}
			items[i].Trends[width] = updated
		}
	}
	return items, nil
}
