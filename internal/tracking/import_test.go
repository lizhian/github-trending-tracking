package tracking_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lizhian/github-trending-tracking/internal/tracking"
	"github.com/lizhian/github-trending-tracking/migrations"
)

func frozenExport(t *testing.T) string {
	t.Helper()
	var b strings.Builder
	for _, name := range tracking.MigrationNames {
		raw, err := migrations.Files.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(raw)
	}
	b.WriteString(`
CREATE TABLE d1_migrations(id INTEGER PRIMARY KEY,name TEXT,applied_at TEXT);
INSERT INTO d1_migrations VALUES(1,'0001_tracking.sql','2026-10-01'),(2,'0002_free_tier.sql','2026-10-01'),(3,'0003_tracking_types.sql','2026-10-01');
CREATE TABLE _cf_KV(key TEXT,value TEXT);
INSERT INTO projects(id,cycle,name,topics,entered_at,listed_at,observed_at,attempted_at,stars,trends,type,repo_id) VALUES('github:R_1','original','acme/one','["tools"]',1,2,3,3,100,'{}','github','R_1');
INSERT INTO snapshot_days VALUES('original',0,'{"0":[3,100]}');
INSERT INTO snapshot_days VALUES('original',-1,'{"-1":[-1,99]}');
INSERT INTO system_state VALUES('sampling_policy','{"version":2,"effectiveAt":123}'),('pending','[{"nextAt":456}]'),('upstream','{"api.github.com":{"blockedUntil":789}}'),('lease','{"owner":"legacy","expires":1000}');
`)
	path := filepath.Join(t.TempDir(), "frozen.sql")
	if err := os.WriteFile(path, []byte(b.String()), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func TestImportPreservesAllPhysicalHistoryAndStateAndAuditsLeaseException(t *testing.T) {
	source := frozenExport(t)
	output := filepath.Join(t.TempDir(), "tracking.sqlite")
	reportPath := filepath.Join(t.TempDir(), "report.json")
	if _, err := tracking.ImportD1(source, output, reportPath, false); err == nil {
		t.Fatal("lease was removed without stopped-writer acknowledgement")
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("failed import published database")
	}
	report, err := tracking.ImportD1(source, output, reportPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Matched || report.Source.Tables["snapshot_days"].Rows != 2 || report.Target.SnapshotPoints != 2 || !report.LeaseCleared {
		t.Fatalf("bad migration report: %+v", report)
	}
	raw, err := os.ReadFile(reportPath)
	if err != nil || !json.Valid(raw) {
		t.Fatal("report missing")
	}
	store, err := tracking.Open(output)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var policy tracking.SamplingPolicy
	if ok, err := store.State("sampling_policy", &policy); err != nil || !ok || policy.EffectiveAt != 123 {
		t.Fatal("policy reset")
	}
	var count int
	if err := store.DB.QueryRow("SELECT count(*) FROM sqlite_master WHERE name IN ('_cf_KV','d1_migrations')").Scan(&count); err != nil || count != 0 {
		t.Fatal("copied D1 internal tables")
	}
	if _, err := tracking.ImportD1(source, output, reportPath, true); err == nil {
		t.Fatal("overwrote existing target")
	}
	raw, _ = os.ReadFile(source)
	bad := filepath.Join(t.TempDir(), "bad.sql")
	os.WriteFile(bad, []byte(strings.ReplaceAll(string(raw), "0003_tracking_types.sql", "unrecognized.sql")), 0600)
	if _, err := tracking.ImportD1(bad, filepath.Join(t.TempDir(), "bad.sqlite"), reportPath, true); err == nil {
		t.Fatal("accepted unsupported migration version")
	}
}
