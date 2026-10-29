package tracking

import (
	"encoding/json"
	"fmt"
	"math"
)

const Hour int64 = 3_600_000
const Day int64 = 24 * Hour
const Retention int64 = 30 * Day
const SamplingGrace int64 = 10 * 60_000

var Windows = []int{24, 72, 168, 720}
var SamplingHours = []int64{1, 5, 9, 11, 13, 15, 17, 19, 21}

type SamplingPolicy struct {
	Version     int   `json:"version"`
	EffectiveAt int64 `json:"effectiveAt"`
}
type Point [2]int64
type Gap [2]int64
type TrendSummary struct {
	Delta *int64 `json:"delta"`
	State string `json:"state"`
	Start *int64 `json:"start"`
	End   *int64 `json:"end"`
	Gaps  []Gap  `json:"gaps"`
}
type Trend struct {
	TrendSummary
	Points []Point `json:"points"`
}

func ptr(n int64) *int64        { return &n }
func floorDiv(a, b int64) int64 { return int64(math.Floor(float64(a) / float64(b))) }
func LatestSlot(at int64) int64 {
	day := floorDiv(at+8*Hour, Day)*Day - 8*Hour
	for i := len(SamplingHours) - 1; i >= 0; i-- {
		slot := day + SamplingHours[i]*Hour
		if slot <= at {
			return slot
		}
	}
	return day - Day + 21*Hour
}
func NextSlot(at int64) int64 {
	day := floorDiv(at+8*Hour, Day)*Day - 8*Hour
	for _, hour := range SamplingHours {
		slot := day + hour*Hour
		if slot > at {
			return slot
		}
	}
	return day + Day + Hour
}
func SamplingSlot(at int64, p *SamplingPolicy) int64 {
	if p == nil || at < p.EffectiveAt {
		return floorDiv(at, Hour) * Hour
	}
	return LatestSlot(at)
}
func observationSlot(at int64, p *SamplingPolicy) int64 {
	if p == nil || at < p.EffectiveAt {
		return floorDiv(at, Hour) * Hour
	}
	slot := LatestSlot(at)
	if slot >= p.EffectiveAt {
		return slot
	}
	return floorDiv(p.EffectiveAt-1, Hour) * Hour
}
func nextExpected(at int64, p *SamplingPolicy) int64 {
	hourly := (floorDiv(at, Hour) + 1) * Hour
	if p == nil || hourly < p.EffectiveAt {
		return hourly
	}
	return NextSlot(max(at, p.EffectiveAt-1))
}
func MissedSampling(from, to int64, p *SamplingPolicy) bool {
	return nextExpected(observationSlot(from, p), p) < observationSlot(to, p)
}
func IsStale(observedAt, now int64, p *SamplingPolicy) bool {
	if p == nil || now < p.EffectiveAt {
		return now-observedAt > 2*Hour
	}
	if observedAt+2*Hour < p.EffectiveAt {
		return true
	}
	return now > NextSlot(observedAt)+SamplingGrace
}
func CompleteWindow(start, end int64, hours int, p *SamplingPolicy) bool {
	boundary := end - int64(hours)*Hour
	latestStart := boundary + Hour
	if p != nil && boundary >= p.EffectiveAt {
		latestStart = NextSlot(boundary-1) + SamplingGrace
	}
	return start <= latestStart
}
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

// ProjectViews retains persisted summaries and only replaces baselines that have
// fallen outside the strict 30-day query horizon, matching the existing reader.
func (s *Store) ProjectViews(rows []ProjectRow, now int64, policy *SamplingPolicy) ([]Project, error) {
	items := make([]Project, 0, len(rows))
	expired := []string{}
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
			trends[width] = trend
		}
		var topics []string
		if err := json.Unmarshal([]byte(row.Topics), &topics); err != nil {
			return nil, err
		}
		if topics == nil {
			topics = []string{}
		}
		items = append(items, Project{ID: row.ID, Type: row.Type, RepoID: row.RepoID, Name: row.Name, Description: row.Description, Homepage: row.Homepage, Language: row.Language, Topics: topics, Stars: row.Stars, EnteredAt: row.EnteredAt, ListedAt: optionalListedAt(row.ListedAt), ObservedAt: row.ObservedAt, AttemptedAt: row.AttemptedAt, Error: row.Error, Stale: IsStale(row.ObservedAt, now, policy), Trends: trends})
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

func optionalListedAt(at int64) *int64 {
	if at == 0 {
		return nil
	}
	return &at
}
