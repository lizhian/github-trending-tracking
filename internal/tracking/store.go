// Package tracking 实现开源项目的发现、收录、Star 观测和查询。
// Go 的包由同一目录下的源文件共同组成；这里的文件不是 Java 中“一文件一个类”的边界。
// 导出名称以大写字母开头，小写名称只在本包可用；internal 目录还限制外部项目导入。
//
// 学习入口：Store 管理 SQLite 与持久状态；http.go 提供查询；scheduler.go 编排采集；
// upstream.go 处理上游请求；observations.go 写入快照；trends.go 计算趋势。
// 本文件主要讲连接、锁、事务和 SQL 到 Go 类型的映射。
package tracking

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/lizhian/github-trending-tracking/migrations"
	"golang.org/x/sys/unix"
	// 匿名导入只为执行驱动包的初始化，注册名为 sqlite 的驱动；本文件不直接引用其导出符号。
	_ "modernc.org/sqlite"
)

// SchemaVersion 是当前运行所要求的 schema；Initialize/Migrate 写入，Open 严格核对。
const SchemaVersion = 6
// DatabaseName 是命令行默认文件名；路径仍可由配置覆盖。
const DatabaseName = "tracking.sqlite"

// 迁移必须按版本顺序执行，文件内容由 migrations.Files 从二进制内读取。
// []string 是 slice，长度可变；这里 var 而非 const，因为 Go 的常量不能是 slice。
var MigrationNames = []string{"0001_tracking.sql", "0002_free_tier.sql", "0003_tracking_types.sql", "0004_submissions.sql", "0005_homepage.sql", "0006_remove_listing_time.sql"}

// Store 持有数据库连接池、目录写锁和进程内缓存版本。
// struct 可类比“带字段的数据类型”，方法在类型外声明；它没有 Java 的继承体系。
// 包含 sync.Once 和 atomic 字段的 Store 使用后不能复制，调用方传递 *Store。
type Store struct {
	// DB 暴露标准 SQL 连接池；lock 保存文件句柄以持续持有内核锁。
	// closeOnce 保证一次清理；listRevision 是原子计数器，让 HTTP 缓存识别数据变化。
	DB           *sql.DB
	lock         *os.File
	closeOnce    sync.Once
	listRevision atomic.Uint64
}

// database 根据文件路径和 SQLite mode 建立连接池并验证连接；rw 不创建缺失文件，ro 为只读。
// sql.Open 返回的 *sql.DB 类似 DataSource/连接池，不是一条 JDBC Connection；Ping 才验证实际连接。
func database(path, mode string) (*sql.DB, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: abs}
	q := u.Query()
	q.Set("mode", mode)
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "busy_timeout(5000)")
	u.RawQuery = q.Encode()
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, err
	}
	// 单连接使事务与每连接 PRAGMA 保持简单；代价是所有数据库访问需要轮流取得这条连接。
	// 尤其不能持有 Rows 或 Tx 时又通过 db 取新连接，否则可能等自己的连接，形成阻塞。
	db.SetMaxOpenConns(1)
	if err = db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// directoryLock 对数据库所在目录获取非阻塞、进程间独占文件锁。
// 返回的文件必须保持打开；同目录多个数据库也共用这把锁，防止多个活跃写入进程。
func directoryLock(path string) (*os.File, error) {
	info, err := os.Stat(filepath.Dir(path))
	if err != nil || !info.IsDir() {
		return nil, fmt.Errorf("data directory must already exist: %s", filepath.Dir(path))
	}
	f, err := os.OpenFile(filepath.Join(filepath.Dir(path), ".writer.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	// LOCK_EX 为独占锁，LOCK_NB 为拿不到就立即报错；| 按位组合标志，不是逻辑或 ||。
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("data directory already has an active writer")
	}
	return f, nil
}

// unlock 释放目录锁并关闭文件；nil 表示没有资源，允许清理路径统一调用。
// _ = 明确丢弃清理错误，并不表示该操作一定成功。
func unlock(f *os.File) {
	if f != nil {
		_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
		_ = f.Close()
	}
}

// Open 打开并校验已有数据库，绝不隐式创建空库。
// 依次获取目录锁、检查 schema、启用 WAL、验证完整性和可写性；任一步失败都会释放已获取资源。
func Open(path string) (*Store, error) {
	lock, err := directoryLock(path)
	if err != nil {
		return nil, err
	}
	db, err := database(path, "rw")
	if err != nil {
		unlock(lock)
		return nil, fmt.Errorf("open existing database: %w", err)
	}
	s := &Store{DB: db, lock: lock}
	// 闭包捕获 s，将后续失败都统一为“关闭已打开资源，再返回错误”。
	// &Store{...} 中未显式填写的 sync.Once、atomic.Uint64 自动采用可用的零值。
	fail := func(err error) (*Store, error) { s.Close(); return nil, err }
	var version int
	if err = db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return fail(err)
	}
	if version != SchemaVersion {
		return fail(fmt.Errorf("unsupported database schema version %d (want %d)", version, SchemaVersion))
	}
	if err = validateColumns(db); err != nil {
		return fail(err)
	}
	var journal string
	if err = db.QueryRow("PRAGMA journal_mode=WAL").Scan(&journal); err != nil {
		return fail(err)
	}
	if journal != "wal" {
		return fail(errors.New("SQLite WAL is required"))
	}
	if err = CheckDatabase(db); err != nil {
		return fail(err)
	}
	// 用不改变任何行的写语句验证挂载库确实可写，避免服务启动后第一次写入才发现权限问题。
	tx, err := db.Begin()
	if err != nil {
		return fail(err)
	}
	if _, err = tx.Exec("UPDATE system_state SET value=value WHERE 0"); err != nil {
		tx.Rollback()
		return fail(err)
	}
	if err = tx.Commit(); err != nil {
		return fail(err)
	}
	return s, nil
}

// Close 关闭连接池并释放目录锁；sync.Once 保证清理动作最多执行一次。
// (s *Store) 是指针接收者，可类比实例方法的 this，但接收者名由作者选择；后续重复调用返回 nil。
func (s *Store) Close() error {
	var err error
	s.closeOnce.Do(func() { err = s.DB.Close(); unlock(s.lock) })
	return err
}

// initializeDB 在一个事务内按顺序执行全部迁移并写入 schema 版本，成功才提交。
// defer tx.Rollback 兜底所有提前返回；Commit 后 Rollback 会返回事务已结束错误，此处忽略。
func initializeDB(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	// 显式事务不像 Spring @Transactional 自动根据异常决定结果：每条失败路径返回 error，
	// Rollback 兜底；只有最终 Commit 成功，全部变更才可见。
	defer tx.Rollback()
	for _, name := range MigrationNames {
		b, err := migrations.Files.ReadFile(name)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(string(b)); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(fmt.Sprintf("PRAGMA user_version=%d", SchemaVersion)); err != nil {
		return err
	}
	return tx.Commit()
}

// Initialize 显式初始化全新数据库，用 O_EXCL 拒绝覆盖现有文件。
// 闭包读取 ok 标志；只有初始化全部成功才保留文件，否则返回时删除本次创建的主文件。
func Initialize(path string) error {
	lock, err := directoryLock(path)
	if err != nil {
		return err
	}
	defer unlock(lock)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	f.Close()
	// ok 被下面 defer 闭包引用；闭包会在返回时读取最终值，不是在登记时固定为 false。
	ok := false
	defer func() {
		if !ok {
			os.Remove(path)
		}
	}()
	db, err := database(path, "rw")
	if err != nil {
		return err
	}
	defer db.Close()
	if err = initializeDB(db); err != nil {
		return err
	}
	ok = true
	return nil
}

// map[string][]string 对应“表名 → 有序列名集合”；嵌套字面量可省略重复的 []string 类型。
// 这些键来自代码内白名单，后续拼表名不接收用户输入；普通值仍用 SQL 参数绑定。
var tableColumns = map[string][]string{
	"projects":      {"id", "cycle", "name", "description", "language", "topics", "entered_at", "observed_at", "attempted_at", "error", "stars", "trends", "metadata_at", "type", "repo_id", "homepage"},
	"snapshot_days": {"cycle", "day", "hours"},
	"system_state":  {"key", "value"},
	"submissions":   {"id", "name", "repo_id", "phase", "github", "linuxdo", "repository_attempts", "readme_attempts", "next_at", "created_at", "finished_at", "message", "merged_into"},
}

// columnsForVersion 返回指定 schema 版本的列清单，兼容旧备份的审计和恢复。
// slices.Clone 先复制底层元素，避免 Insert 等操作影响全局清单；旧 listed_at 只属于兼容路径。
func columnsForVersion(table string, version int) []string {
	columns := slices.Clone(tableColumns[table])
	if table == "projects" && version < 6 {
		columns = slices.Insert(columns, 7, "listed_at")
	}
	if table == "projects" && version < 5 {
		return columns[:len(columns)-1]
	}
	return columns
}

// validateColumns 按版本逐表核对列名与顺序，避免用不匹配的 schema 扫描数据。
// 遍历每张表后立即关闭 Rows，释放单连接池中的连接，再查询下一张表。
func validateColumns(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	for table := range tableColumns {
		want := columnsForVersion(table, version)
		if table == "submissions" && version < 4 {
			continue
		}
		rows, err := db.Query("PRAGMA table_info(" + table + ")")
		if err != nil {
			return err
		}
		got := []string{}
		for rows.Next() {
			var cid, notnull, pk int
			var name, kind string
			var def any
			if err = rows.Scan(&cid, &name, &kind, &notnull, &def, &pk); err != nil {
				rows.Close()
				return err
			}
			got = append(got, name)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		if !slices.Equal(got, want) {
			return fmt.Errorf("unexpected %s schema columns: %v", table, got)
		}
	}
	return nil
}

// CheckDatabase 执行 SQLite 完整性与外键检查；无错误时返回 nil。
// rows.Next 为 false 可能是遍历结束，也可能是读取失败，因此最后还要检查 rows.Err。
func CheckDatabase(db *sql.DB) error {
	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("SQLite integrity check failed: %s", result)
	}
	rows, err := db.Query("PRAGMA foreign_key_check")
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return errors.New("SQLite foreign key check failed")
	}
	return rows.Err()
}

// State 按 key 解码持久化 JSON 到 out；out 应传入可写指针，如 &policy。
// 返回 (false, nil) 表示键不存在；(true, err) 仍可能表示 JSON 解码失败，不能只看 bool。
func (s *Store) State(key string, out any) (bool, error) {
	var raw string
	// QueryRow 的查询错误通常延迟到 Scan 返回；sql.ErrNoRows 在本函数中表示未找到，并非系统故障。
	err := s.DB.QueryRow("SELECT value FROM system_state WHERE key=?", key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal([]byte(raw), out)
}

// SetState 将任意可 JSON 编码的值保存为持久状态；同 key 使用 upsert 替换。
// any 是 interface{} 的别名，类似可容纳多种运行时值的 Object，具体能否编码仍由 json.Marshal 检查。
func (s *Store) SetState(key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = s.DB.Exec("INSERT INTO system_state(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", key, string(b))
	return err
}

// Policy 读取采样规则；尚无规则时返回 (nil, nil)，由调度或投稿在需要时建立。
func (s *Store) Policy() (*SamplingPolicy, error) {
	var policy SamplingPolicy
	ok, err := s.State("sampling_policy", &policy)
	if err != nil || !ok {
		return nil, err
	}
	return &policy, nil
}

// ProjectRow 对应数据库项目行，Topics/Trends 暂存 JSON 文本；对外展示模型是 http.go 的 Project。
// *string 允许区分 SQL NULL 与空字符串；int64 时间字段均使用 Unix 毫秒。
type ProjectRow struct {
	ID, Cycle, Name                    string
	Description, Language, Homepage    *string
	Topics                             string
	EnteredAt, ObservedAt, AttemptedAt int64
	Error                              *string
	Stars                              int64
	Trends                             string
	MetadataAt                         int64
	Type, RepoID                       string
}

const projectColumns = "id,cycle,name,description,language,topics,entered_at,observed_at,attempted_at,error,stars,trends,metadata_at,type,repo_id,homepage"

// scanProject 通过最小 Scan 接口复用 *sql.Row 和 *sql.Rows，无需它们声明 implements。
// & 把字段地址交给 Scan 写入；末尾裸 return 返回命名返回值 p、err 的当前值。
func scanProject(row interface{ Scan(...any) error }) (p ProjectRow, err error) {
	// 这些指针必须与 projectColumns 列顺序一致；可空 *string 字段传 &p.Description 是 **string，
	// database/sql 能据 NULL 或文本设置字段，而不是把 NULL 错当空字符串。
	err = row.Scan(&p.ID, &p.Cycle, &p.Name, &p.Description, &p.Language, &p.Topics, &p.EnteredAt, &p.ObservedAt, &p.AttemptedAt, &p.Error, &p.Stars, &p.Trends, &p.MetadataAt, &p.Type, &p.RepoID, &p.Homepage)
	return
}

// ProjectRows 用内部提供的 SQL 条件查询项目；值通过 ? 参数绑定，where 只能由可信代码构造。
// args ...any 是可变参数，转发时 args... 将 slice 展开；空结果是非 nil 的空 slice。
func (s *Store) ProjectRows(where string, args ...any) ([]ProjectRow, error) {
	rows, err := s.DB.Query("SELECT "+projectColumns+" FROM projects "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []ProjectRow{}
	for rows.Next() {
		p, err := scanProject(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, p)
	}
	return result, rows.Err()
}

// Histories 每批最多查询 90 个追踪周期，返回最近 30 天、按时间升序排列的快照。
// 数据库按 UTC 日/小时存 JSON 桶；map 遍历没有顺序保证，所以读出后必须显式排序。
func (s *Store) Histories(cycles []string, now int64) (map[string][]Point, error) {
	output := map[string][]Point{}
	// 先分批限制 SQL 参数规模；make([]any, 0, n) 是长度为 0、预留容量 n，append 会增长长度。
	// slice 复制只复制描述信息，元素可能共享底层数组；range 取得的普通元素是值副本。
	for start := 0; start < len(cycles); start += 90 {
		group := cycles[start:min(start+90, len(cycles))]
		args := make([]any, 0, len(group)+1)
		for _, cycle := range group {
			args = append(args, cycle)
		}
		args = append(args, (now-Retention)/Day)
		rows, err := s.DB.Query("SELECT cycle,hours FROM snapshot_days WHERE cycle IN ("+strings.TrimSuffix(strings.Repeat("?,", len(group)), ",")+") AND day>=? ORDER BY day", args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var cycle, raw string
			if err = rows.Scan(&cycle, &raw); err != nil {
				rows.Close()
				return nil, err
			}
			var hours map[string]Point
			if err = json.Unmarshal([]byte(raw), &hours); err != nil {
				rows.Close()
				return nil, err
			}
			for _, p := range hours {
				if p[0] >= now-Retention && p[0] <= now {
					output[cycle] = append(output[cycle], p)
				}
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	for _, points := range output {
		slices.SortFunc(points, func(a, b Point) int {
			if a[0] < b[0] {
				return -1
			}
			if a[0] > b[0] {
				return 1
			}
			return 0
		})
	}
	return output, nil
}
