package tracking

import (
	"database/sql"
	"errors"
	"net/http"
	"time"
)

// VisitCounts 是全站 PV；日界线固定为北京时间，不依赖服务器时区。
type VisitCounts struct {
	Today int64 `json:"today"`
	Total int64 `json:"total"`
}

func (h *handler) visitsAPI(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodOptions {
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST, OPTIONS")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "仅支持 GET 或 POST"})
		return
	}
	day := h.options.Now().In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("2006-01-02")
	var counts VisitCounts
	var err error
	if r.Method == http.MethodPost {
		// 单条 UPSERT 同时更新并返回两个计数，并发访问不会发生读改写丢失。
		err = h.store.DB.QueryRowContext(r.Context(), `
			INSERT INTO system_state(key,value)
			VALUES('page_visits',json_object('day',?,'today',1,'total',1))
			ON CONFLICT(key) DO UPDATE SET value=json_object(
				'day',?,
				'today',CASE WHEN json_extract(value,'$.day')=? THEN json_extract(value,'$.today')+1 ELSE 1 END,
				'total',json_extract(value,'$.total')+1)
			RETURNING json_extract(value,'$.today'),json_extract(value,'$.total')`, day, day, day).Scan(&counts.Today, &counts.Total)
	} else {
		err = h.store.DB.QueryRowContext(r.Context(), `
			SELECT CASE WHEN json_extract(value,'$.day')=? THEN json_extract(value,'$.today') ELSE 0 END,
			json_extract(value,'$.total') FROM system_state WHERE key='page_visits'`, day).Scan(&counts.Today, &counts.Total)
		if errors.Is(err, sql.ErrNoRows) {
			err = nil
		}
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "访问统计暂不可用"})
		return
	}
	writeJSON(w, http.StatusOK, counts)
}
