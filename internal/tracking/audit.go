package tracking

// 本文件审计整个物理数据集，用于备份、恢复与 verify 命令。
// 与只查最近 30 天的页面查询不同，这里也检查更早历史；继续可阅读 backup.go。

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// TableDigest 保存一张表的行数和按稳定行序生成的内容摘要，用于比较备份前后数据。
type TableDigest struct {
	Rows   int    `json:"rows"`
	SHA256 string `json:"sha256"`
}

// ProjectCheckpoint 记录追踪项目身份、追踪周期和入库时间，用于检查恢复是否保留原周期。
type ProjectCheckpoint struct {
	ID        string `json:"id"`
	Cycle     string `json:"cycle"`
	EnteredAt int64  `json:"enteredAt"`
}

// DatabaseAudit 汇总表摘要、项目/快照数量和关键调度状态；可空时间在没有快照时为 nil。
type DatabaseAudit struct {
	Tables           map[string]TableDigest `json:"tables"`
	ProjectTypes     map[string]int         `json:"projectTypes"`
	Repositories     int                    `json:"repositories"`
	Cycles           int                    `json:"cycles"`
	SnapshotPoints   int                    `json:"snapshotPoints"`
	Earliest         *int64                 `json:"earliest"`
	Latest           *int64                 `json:"latest"`
	Projects         []ProjectCheckpoint    `json:"projects"`
	StateCheckpoints map[string]any         `json:"stateCheckpoints"`
	Integrity        string                 `json:"integrity"`
	ForeignKeys      string                 `json:"foreignKeys"`
}

// Audit 检查整个物理数据集，包括查询窗口之外的旧历史，返回可用于恢复比对的报告。
// JSON 先解码再编码后计算摘要，避免对象键顺序不同造成误报；调用方负责使用一致的数据视图。
func Audit(db *sql.DB) (DatabaseAudit, error) {
	report := DatabaseAudit{Tables: map[string]TableDigest{}, ProjectTypes: map[string]int{}, Projects: []ProjectCheckpoint{}, StateCheckpoints: map[string]any{}}
	if err := CheckDatabase(db); err != nil {
		return report, err
	}
	report.Integrity = "ok"
	report.ForeignKeys = "ok"
	tables := []string{"projects", "snapshot_days", "system_state"}
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return report, err
	}
	if version >= 4 {
		tables = append(tables, "submissions")
	}
	for _, table := range tables {
		columns := columnsForVersion(table, version)
		query := "SELECT " + strings.Join(columns, ",") + " FROM " + table
		switch table {
		case "projects", "submissions":
			query += " ORDER BY id"
		case "snapshot_days":
			query += " ORDER BY cycle,day"
		case "system_state":
			query += " ORDER BY key"
		}
		rows, err := db.Query(query)
		if err != nil {
			return report, err
		}
		hash := sha256.New()
		count := 0
		for rows.Next() {
			// Scan 需要一组目标地址，所以另建 pointers 存每个 values 元素的地址；
			// pointers... 将 slice 展开为可变参数，扫描后 values 保存真实列值。
			values := make([]any, len(columns))
			pointers := make([]any, len(values))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err = rows.Scan(pointers...); err != nil {
				rows.Close()
				return report, err
			}
			for i, column := range columns {
				// v, ok := x.(T) 是安全类型断言；ok=false 不会 panic。驱动返回的字节文本先统一为 string。
				if value, ok := values[i].([]byte); ok {
					values[i] = string(value)
				}
				if slices.Contains([]string{"topics", "trends", "hours", "value"}, column) {
					raw, ok := values[i].(string)
					if !ok {
						rows.Close()
						return report, fmt.Errorf("%s.%s is not JSON text", table, column)
					}
					var decoded any
					if err = json.Unmarshal([]byte(raw), &decoded); err != nil {
						rows.Close()
						return report, fmt.Errorf("invalid %s.%s JSON", table, column)
					}
					values[i] = decoded
				}
			}
			// 稳定行序 + 规范化 JSON 生成逐行摘要；
			// Go map 遍历无序，但 encoding/json 对受支持的 map 键排序编码，不直接按遍历顺序拼文本。
			raw, err := json.Marshal(values)
			if err != nil {
				rows.Close()
				return report, err
			}
			hash.Write(raw)
			hash.Write([]byte{'\n'})
			count++
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return report, err
		}
		report.Tables[table] = TableDigest{Rows: count, SHA256: hex.EncodeToString(hash.Sum(nil))}
	}
	rows, err := db.Query("SELECT id,cycle,entered_at,type,repo_id FROM projects ORDER BY id")
	if err != nil {
		return report, err
	}
	repos, cycles := map[string]bool{}, map[string]bool{}
	for rows.Next() {
		var p ProjectCheckpoint
		var kind, repo string
		if err = rows.Scan(&p.ID, &p.Cycle, &p.EnteredAt, &kind, &repo); err != nil {
			rows.Close()
			return report, err
		}
		report.Projects = append(report.Projects, p)
		report.ProjectTypes[kind]++
		repos[repo] = true
		cycles[p.Cycle] = true
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return report, err
	}
	report.Repositories = len(repos)
	report.Cycles = len(cycles)
	rows, err = db.Query("SELECT day,hours FROM snapshot_days ORDER BY cycle,day")
	if err != nil {
		return report, err
	}
	for rows.Next() {
		var day int64
		var raw string
		if err = rows.Scan(&day, &raw); err != nil {
			rows.Close()
			return report, err
		}
		var hours map[string]Point
		if err = json.Unmarshal([]byte(raw), &hours); err != nil {
			rows.Close()
			return report, err
		}
		for key, point := range hours {
			hour, err := strconv.ParseInt(key, 10, 64)
			if err != nil || floorDiv(point[0], Day) != day || floorDiv(point[0], Hour) != hour || point[1] < 0 {
				rows.Close()
				return report, errors.New("invalid UTC snapshot bucket or point")
			}
			report.SnapshotPoints++
			if report.Earliest == nil || point[0] < *report.Earliest {
				report.Earliest = ptr(point[0])
			}
			if report.Latest == nil || point[0] > *report.Latest {
				report.Latest = ptr(point[0])
			}
		}
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return report, err
	}
	rows, err = db.Query("SELECT key,value FROM system_state ORDER BY key")
	if err != nil {
		return report, err
	}
	for rows.Next() {
		var key, raw string
		if err = rows.Scan(&key, &raw); err != nil {
			rows.Close()
			return report, err
		}
		var value any
		if err = json.Unmarshal([]byte(raw), &value); err != nil {
			rows.Close()
			return report, err
		}
		auditStateCheckpoints(report.StateCheckpoints, key, value)
		if key == linuxdoCleanupKey {
			report.StateCheckpoints[key] = value
		}
	}
	err = rows.Err()
	rows.Close()
	return report, err
}

// auditStateCheckpoints 递归遍历 JSON 对象和数组，提取生效边界、重试与主机冷却时间。
// switch value := value.(type) 是类型分支：每个 case 中 value 变为对应具体类型，类似 instanceof 后转型。
func auditStateCheckpoints(out map[string]any, path string, value any) {
	switch value := value.(type) {
	case map[string]any:
		for key, child := range value {
			next := path + "." + key
			if slices.Contains([]string{"effectiveAt", "nextAt", "readmeNextAt", "blockedUntil"}, key) {
				out[next] = child
			}
			auditStateCheckpoints(out, next, child)
		}
	case []any:
		for i, child := range value {
			auditStateCheckpoints(out, fmt.Sprintf("%s[%d]", path, i), child)
		}
	}
}
