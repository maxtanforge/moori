package storage

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestPruneTemplateVersionsKeepsNewestWindow(t *testing.T) {
	ctx := context.Background()
	store := openTestStore(t, filepath.Join(t.TempDir(), "control.db"))
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UnixMilli()
	_, err := store.db.Exec(`INSERT INTO console_templates(id,name,description) VALUES ('t','T',''),('u','U','')`)
	must(err)
	for version := 1; version <= 25; version++ {
		_, err := store.db.Exec(`INSERT INTO console_template_versions(template_id,version,compose,digest,parameters,secret_services,created_ms)
 VALUES ('t',?,'compose','d','[]','{}',?)`, version, now)
		must(err)
	}
	for version := 1; version <= 3; version++ {
		_, err := store.db.Exec(`INSERT INTO console_template_versions(template_id,version,compose,digest,parameters,secret_services,created_ms)
 VALUES ('u',?,'compose','d','[]','{}',?)`, version, now)
		must(err)
	}
	const retain = 7
	must(store.PruneTemplateVersions(ctx, retain))
	var count int
	must(store.db.QueryRow(`SELECT count(*) FROM console_template_versions WHERE template_id='t'`).Scan(&count))
	if count != retain {
		t.Fatal(count)
	}
	var oldest int
	must(store.db.QueryRow(`SELECT min(version) FROM console_template_versions WHERE template_id='t'`).Scan(&oldest))
	if oldest != 25-retain+1 {
		t.Fatal(oldest)
	}
	must(store.db.QueryRow(`SELECT count(*) FROM console_template_versions WHERE template_id='u'`).Scan(&count))
	if count != 3 {
		t.Fatal(count)
	}
}
