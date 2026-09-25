package api

import (
	"errors"
	"net/http"
	"testing"
)

// Each disk-change refusal comes back typed, with the server's code and
// sentence; disk_too_small also carries the sizes.
func TestRetierDatabase_DiskChangeRefusals(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		want       DiskChangeError
	}{
		{"disk_too_small",
			`{"code":"disk_too_small","error":"does not fit in 1 GB","disk_used_bytes":1572864000,"min_disk_gb":2,"target_disk_gb":1}`,
			http.StatusConflict,
			DiskChangeError{Status: 409, Code: CodeDiskTooSmall, Message: "does not fit in 1 GB", DiskUsedBytes: 1572864000, MinDiskGB: 2, TargetDiskGB: 1}},
		{"resize_in_progress", `{"code":"resize_in_progress","error":"a disk resize is in progress"}`,
			http.StatusConflict, DiskChangeError{Status: 409, Code: CodeResizeInProgress, Message: "a disk resize is in progress"}},
		{"database_not_running", `{"code":"database_not_running","error":"the database must be running to change its disk"}`,
			http.StatusConflict, DiskChangeError{Status: 409, Code: CodeDatabaseNotRunning, Message: "the database must be running to change its disk"}},
		{"storage_capacity", `{"code":"storage_capacity","error":"the platform does not have the storage for this disk right now"}`,
			http.StatusServiceUnavailable, DiskChangeError{Status: 503, Code: CodeStorageCapacity, Message: "the platform does not have the storage for this disk right now"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := jsonStatusServer(t, tc.status, tc.body)
			_, err := newTestClient(ts.URL).RetierDatabase("d1", "", 1, "")
			var de *DiskChangeError
			if !errors.As(err, &de) {
				t.Fatalf("err = %#v; want *DiskChangeError", err)
			}
			if *de != tc.want {
				t.Errorf("err = %+v; want %+v", *de, tc.want)
			}
			// A storage refusal is not the points marketplace's capacity class.
			var me *MarketplaceError
			if errors.As(err, &me) {
				t.Errorf("err = %v; must not be a *MarketplaceError", err)
			}
		})
	}
}

// Code-less errors keep their old classes: the 400 below 1 GB is plain text,
// and a points refusal stays a marketplace error.
func TestRetierDatabase_CodelessErrorsUnchanged(t *testing.T) {
	ts := jsonStatusServer(t, http.StatusBadRequest, `{"error":"a database disk is at least 1 GB"}`)
	_, err := newTestClient(ts.URL).RetierDatabase("d1", "", 1, "")
	var de *DiskChangeError
	if err == nil || errors.As(err, &de) || err.Error() != "a database disk is at least 1 GB" {
		t.Errorf("400 = %#v; want the plain server message", err)
	}

	ts = jsonStatusServer(t, http.StatusConflict, `{"error":"this change would exceed your plan's points budget"}`)
	_, err = newTestClient(ts.URL).RetierDatabase("d1", "", 20, "")
	var me *MarketplaceError
	if !errors.As(err, &me) || me.Kind != "insufficient" {
		t.Errorf("points 409 = %#v; want *MarketplaceError insufficient", err)
	}
}

func TestGetDatabase_ParsesTheDiskChange(t *testing.T) {
	ts := jsonStatusServer(t, http.StatusOK, `{"database":{"id":"d1","name":"pg","status":"resizing","disk_gb":10,`+
		`"disk_used_bytes":1288490188,"min_disk_gb":2,`+
		`"resize":{"direction":"shrink","target_disk_gb":2,"phase":"copy_out","started_at":"2026-09-25T10:00:00Z"}}}`)

	db, err := newTestClient(ts.URL).GetDatabase("d1")
	if err != nil {
		t.Fatalf("GetDatabase: %v", err)
	}
	if db.Status != "resizing" || db.DiskGB != 10 || db.DiskUsedBytes != 1288490188 {
		t.Errorf("db = %+v", db)
	}
	if db.MinDiskGB == nil || *db.MinDiskGB != 2 {
		t.Errorf("min_disk_gb = %v; want 2", db.MinDiskGB)
	}
	r := db.Resize
	if r == nil || r.Direction != "shrink" || r.TargetDiskGB != 2 || r.Phase != "copy_out" || r.StartedAt == nil {
		t.Errorf("resize = %+v", r)
	}

	// Absent fields stay absent: no reading, no change.
	ts = jsonStatusServer(t, http.StatusOK, `{"database":{"id":"d1","status":"running","disk_gb":10}}`)
	if db, err = newTestClient(ts.URL).GetDatabase("d1"); err != nil || db.MinDiskGB != nil || db.Resize != nil {
		t.Errorf("plain db = %+v, %v; want no min_disk_gb and no resize", db, err)
	}
}

// Both calls keep the server's own sentence rather than a raw body.
func TestGetAndDeleteDatabase_KeepTheServerMessage(t *testing.T) {
	ts := jsonStatusServer(t, http.StatusNotFound, `{"error":"database not found"}`)
	if _, err := newTestClient(ts.URL).GetDatabase("d1"); err == nil || err.Error() != "database not found" {
		t.Errorf("GetDatabase 404 = %v", err)
	}

	ts = jsonStatusServer(t, http.StatusConflict, `{"code":"resize_in_progress","error":"a disk resize is in progress; try again when it has finished"}`)
	err := newTestClient(ts.URL).DeleteDatabase("d1")
	if err == nil || err.Error() != "a disk resize is in progress; try again when it has finished" {
		t.Errorf("DeleteDatabase 409 = %v", err)
	}

	ts = jsonStatusServer(t, http.StatusOK, `{"message":"deleted"}`)
	if err := newTestClient(ts.URL).DeleteDatabase("d1"); err != nil {
		t.Errorf("DeleteDatabase 200 = %v; want nil", err)
	}
}
