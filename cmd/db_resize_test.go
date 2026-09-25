package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"paas-cli/internal/api"
)

// Disk resizing both ways (backend "db disk resize"): the request starts a
// change that runs on the platform, so the command's job is to say what will
// happen, follow it, and end on the new size or the change's own error.

// pgRow is database d1 ("pg") as the API returns it; resize is the raw resize
// object, or "" for none.
func pgRow(status string, diskGB int, resize string) string {
	row := fmt.Sprintf(`"id":"d1","name":"pg","type":"postgres","status":%q,"disk_gb":%d,"tier_slug":"s","backup_tier_slug":"weekly"`, status, diskGB)
	if resize != "" {
		row += `,"resize":` + resize
	}
	return "{" + row + "}"
}

func pgReply(status string, diskGB int, resize string) string {
	return `{"database":` + pgRow(status, diskGB, resize) + `}`
}

// dbStub serves the database list, the resize PATCH and the polled GET. The
// GET replies are played in order and the last one repeats.
type dbStub struct {
	mu       sync.Mutex
	requests []string
	patch    map[string]any
}

func (s *dbStub) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

func (s *dbStub) patchBody() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.patch
}

func newDBStub(t *testing.T, list string, patchStatus int, patchReply string, polls ...string) (*httptest.Server, *dbStub) {
	t.Helper()
	stub := &dbStub{}
	next := 0
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stub.mu.Lock()
		defer stub.mu.Unlock()
		stub.requests = append(stub.requests, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/databases":
			io.WriteString(w, `{"databases":[`+list+`]}`)
		case r.Method == http.MethodPatch && r.URL.Path == "/api/v1/databases/d1/tier":
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &stub.patch)
			w.WriteHeader(patchStatus)
			io.WriteString(w, patchReply)
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/databases/d1" && len(polls) > 0:
			io.WriteString(w, polls[min(next, len(polls)-1)])
			next++
		default:
			w.WriteHeader(http.StatusNotFound)
			io.WriteString(w, `{"error":"unexpected `+r.Method+" "+r.URL.Path+`"}`)
		}
	}))
	t.Cleanup(ts.Close)
	return ts, stub
}

// fastResizeWait shortens the poll so a test does not sit out real 5 s polls.
func fastResizeWait(t *testing.T, timeout time.Duration) {
	t.Helper()
	every, limit := dbResizePollInterval, dbResizeWaitTimeout
	t.Cleanup(func() { dbResizePollInterval, dbResizeWaitTimeout = every, limit })
	dbResizePollInterval = time.Millisecond
	dbResizeWaitTimeout = timeout
}

func countRequests(seen []string, want string) int {
	n := 0
	for _, s := range seen {
		if s == want {
			n++
		}
	}
	return n
}

func TestDiskPhaseLabels(t *testing.T) {
	want := map[string]string{
		"expand":    "growing the disk",
		"stop":      "stopping the database",
		"copy_out":  "copying the data to the new disk",
		"release":   "releasing the old disk",
		"recreate":  "creating the new disk",
		"copy_back": "copying the data onto the new disk",
		"start":     "starting the database",
		"cleanup":   "finishing up",
	}
	for phase, label := range want {
		if got := diskPhaseLabel(phase); got != label {
			t.Errorf("diskPhaseLabel(%q) = %q; want %q", phase, got, label)
		}
	}
	if got := diskPhaseLabel("defrag"); got != "defrag" {
		t.Errorf("unknown phase = %q; want the raw phase", got)
	}
}

func TestDBStatusText(t *testing.T) {
	cases := []struct {
		name string
		db   api.DatabaseInfo
		want string
	}{
		{"plain", api.DatabaseInfo{Status: "running"}, "running"},
		{"resizing", api.DatabaseInfo{Status: "resizing", Resize: &api.DatabaseResize{Direction: "shrink", TargetDiskGB: 2, Phase: "copy_back"}},
			"resizing to 2 GB · copying the data onto the new disk"},
		{"resizing without a phase", api.DatabaseInfo{Status: "resizing", Resize: &api.DatabaseResize{TargetDiskGB: 20}}, "resizing to 20 GB"},
		{"undone", api.DatabaseInfo{Status: "running", Resize: &api.DatabaseResize{TargetDiskGB: 2, Error: "the move to 2 GB failed\nwhile copying"}},
			"running · disk change to 2 GB failed: the move to 2 GB failed while copying"},
		{"needs support", api.DatabaseInfo{Status: "error", Resize: &api.DatabaseResize{TargetDiskGB: 2, Error: "an operator must finish the move"}},
			"error · disk change to 2 GB failed: an operator must finish the move"},
	}
	for _, c := range cases {
		if got := dbStatusText(c.db); got != c.want {
			t.Errorf("%s: dbStatusText = %q; want %q", c.name, got, c.want)
		}
	}
}

func TestResizeErrorText(t *testing.T) {
	small := &api.DiskChangeError{Code: api.CodeDiskTooSmall, Message: "the database uses 1500 MB on disk", MinDiskGB: 2}
	if got := resizeErrorText(small, 10); got != "the database uses 1500 MB on disk\n   Smallest possible now: 2 GB" {
		t.Errorf("disk_too_small = %q", got)
	}
	// The smallest size can exceed the current disk: then no shrink is possible.
	small.MinDiskGB = 11
	if got := resizeErrorText(small, 10); !strings.Contains(got, "Smallest possible now: 11 GB (the disk is 10 GB, so it cannot shrink yet)") {
		t.Errorf("min above current = %q", got)
	}
	for _, code := range []string{api.CodeResizeInProgress, api.CodeDatabaseNotRunning, api.CodeStorageCapacity} {
		if got := resizeErrorText(&api.DiskChangeError{Code: code, Message: "server says " + code}, 10); got != "server says "+code {
			t.Errorf("%s = %q; want the server's message verbatim", code, got)
		}
	}
	// Anything else keeps the marketplace rendering.
	if got := resizeErrorText(errors.New("a database disk is at least 1 GB"), 10); got != "a database disk is at least 1 GB" {
		t.Errorf("plain error = %q", got)
	}
	if got := resizeErrorText(&api.MarketplaceError{Kind: "insufficient", Message: "over budget"}, 10); !strings.Contains(got, "PAYG") {
		t.Errorf("points error = %q; want the insufficient guidance", got)
	}
}

// TestDBResize_ShrinkWaitsThroughEachPhase: a shrink is sent (no client-side
// grow-only refusal), announced, followed phase by phase, and ends on the new
// size.
func TestDBResize_ShrinkWaitsThroughEachPhase(t *testing.T) {
	ts, stub := newDBStub(t, pgRow("running", 10, ""), http.StatusOK,
		pgReply("resizing", 10, `{"direction":"shrink","target_disk_gb":2,"phase":"stop"}`),
		pgReply("resizing", 10, `{"direction":"shrink","target_disk_gb":2,"phase":"copy_out"}`),
		pgReply("resizing", 10, `{"direction":"shrink","target_disk_gb":2,"phase":"copy_out"}`),
		pgReply("resizing", 10, `{"direction":"shrink","target_disk_gb":2,"phase":"start"}`),
		pgReply("running", 2, ""),
	)
	cliHome(t, ts.URL)
	fastResizeWait(t, time.Second)

	out := runCLI(t, t.TempDir(), "db", "resize", "pg", "--disk-gb", "2")

	body := stub.patchBody()
	if body["disk_gb"] != float64(2) || len(body) != 1 {
		t.Errorf("PATCH body = %v; want only disk_gb 2", body)
	}
	if !strings.Contains(out, "Shrinking to 2 GB: the database stops for about a minute while its data moves to the smaller disk.\n") {
		t.Errorf("output = %q; want the shrink headline", out)
	}
	steps := []string{"⏳ stopping the database\n", "⏳ copying the data to the new disk\n", "⏳ starting the database\n", "✅ Disk is now 2 GB\n"}
	at := 0
	for _, step := range steps {
		i := strings.Index(out[at:], step)
		if i < 0 {
			t.Fatalf("output = %q; want %q after position %d", out, step, at)
		}
		at += i + len(step)
	}
	if strings.Count(out, "copying the data to the new disk") != 1 {
		t.Errorf("output = %q; a phase is printed once, however many polls see it", out)
	}
	if strings.Contains(out, "Database 'pg' updated") {
		t.Errorf("output = %q; a disk-only change has no tier/backup summary", out)
	}
	if lastExitCode != 0 {
		t.Errorf("exit code = %d; want 0", lastExitCode)
	}
}

// TestDBResize_GrowWithTierChange: the tier change is applied at once and
// summarized; the grow is online.
func TestDBResize_GrowWithTierChange(t *testing.T) {
	grown := strings.Replace(pgReply("running", 20, ""), `"tier_slug":"s"`, `"tier_slug":"m"`, 1)
	ts, stub := newDBStub(t, pgRow("running", 10, ""), http.StatusOK,
		strings.Replace(pgReply("resizing", 10, `{"direction":"grow","target_disk_gb":20,"phase":"expand"}`), `"tier_slug":"s"`, `"tier_slug":"m"`, 1),
		grown,
	)
	cliHome(t, ts.URL)
	fastResizeWait(t, time.Second)

	out := runCLI(t, t.TempDir(), "db", "resize", "pg", "--tier", "m", "--disk-gb", "20")

	if body := stub.patchBody(); body["tier"] != "m" || body["disk_gb"] != float64(20) {
		t.Errorf("PATCH body = %v; want tier m and disk_gb 20", body)
	}
	for _, want := range []string{"✅ Database 'pg' updated\n", "   Tier:    m\n", "Growing to 20 GB: the database stays online.\n", "⏳ growing the disk\n", "✅ Disk is now 20 GB\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q; want %q", out, want)
		}
	}
	if lastExitCode != 0 {
		t.Errorf("exit code = %d; want 0", lastExitCode)
	}
}

// TestDBResize_FailedChangeExitsNonZero: the change's own error ends the wait,
// and a database left in "error" is sent to support.
func TestDBResize_FailedChangeExitsNonZero(t *testing.T) {
	for _, tc := range []struct {
		status      string
		wantSupport bool
	}{{"running", false}, {"error", true}} {
		t.Run(tc.status, func(t *testing.T) {
			failure := "the move to 2 GB failed while copying the data to the new disk and was undone: copy job failed"
			ts, _ := newDBStub(t, pgRow("running", 10, ""), http.StatusOK,
				pgReply("resizing", 10, `{"direction":"shrink","target_disk_gb":2,"phase":"stop"}`),
				pgReply(tc.status, 10, `{"direction":"shrink","target_disk_gb":2,"error":"`+failure+`"}`),
			)
			cliHome(t, ts.URL)
			fastResizeWait(t, time.Second)

			out := runCLI(t, t.TempDir(), "db", "resize", "pg", "--disk-gb", "2")

			if !strings.Contains(out, "❌ "+failure+"\n") {
				t.Errorf("output = %q; want the change's own error", out)
			}
			if got := strings.Contains(out, "contact support"); got != tc.wantSupport {
				t.Errorf("output = %q; support hint shown = %v, want %v", out, got, tc.wantSupport)
			}
			if strings.Contains(out, "Disk is now") {
				t.Errorf("output = %q; a failed change must not claim a new size", out)
			}
			if lastExitCode != 1 {
				t.Errorf("exit code = %d; want 1", lastExitCode)
			}
		})
	}
}

func TestDBResize_NoWaitReturnsAfterTheRequest(t *testing.T) {
	ts, stub := newDBStub(t, pgRow("running", 10, ""), http.StatusOK,
		pgReply("resizing", 10, `{"direction":"shrink","target_disk_gb":2,"phase":"stop"}`),
		pgReply("running", 2, ""),
	)
	cliHome(t, ts.URL)
	fastResizeWait(t, time.Second)

	out := runCLI(t, t.TempDir(), "db", "resize", "pg", "--disk-gb", "2", "--no-wait")

	if !strings.Contains(out, "Shrinking to 2 GB") || !strings.Contains(out, "ghayma db info pg") {
		t.Errorf("output = %q; want the headline and how to check", out)
	}
	if n := countRequests(stub.seen(), "GET /api/v1/databases/d1"); n != 0 {
		t.Errorf("polled %d times; --no-wait must not poll", n)
	}
	if strings.Contains(out, "⏳") {
		t.Errorf("output = %q; --no-wait follows no phases", out)
	}
	if lastExitCode != 0 {
		t.Errorf("exit code = %d; want 0", lastExitCode)
	}
}

func TestDBResize_WaitGivesUpAfterTheTimeout(t *testing.T) {
	ts, stub := newDBStub(t, pgRow("running", 10, ""), http.StatusOK,
		pgReply("resizing", 10, `{"direction":"shrink","target_disk_gb":2,"phase":"stop"}`),
		pgReply("resizing", 10, `{"direction":"shrink","target_disk_gb":2,"phase":"copy_out"}`),
	)
	cliHome(t, ts.URL)
	fastResizeWait(t, 5*time.Millisecond)

	out := runCLI(t, t.TempDir(), "db", "resize", "pg", "--disk-gb", "2")

	if !strings.Contains(out, "Still resizing") || !strings.Contains(out, "Check it with: ghayma db info pg") {
		t.Errorf("output = %q; want the give-up line and how to check", out)
	}
	if n := countRequests(stub.seen(), "GET /api/v1/databases/d1"); n != 5 {
		t.Errorf("polled %d times; want timeout/interval = 5", n)
	}
	if lastExitCode != 1 {
		t.Errorf("exit code = %d; want 1", lastExitCode)
	}
}

// TestDBResize_RefusalsPrintTheServerMessage covers every refusal of the disk
// change, and that points errors keep their rendering.
func TestDBResize_RefusalsPrintTheServerMessage(t *testing.T) {
	cases := []struct {
		name, reply string
		status      int
		want        []string
	}{
		{"disk_too_small", `{"code":"disk_too_small","error":"the database uses 1500 MB on disk, which does not fit in 1 GB","disk_used_bytes":1572864000,"min_disk_gb":2,"target_disk_gb":1}`,
			http.StatusConflict, []string{"❌ Failed to resize database: the database uses 1500 MB on disk, which does not fit in 1 GB\n", "   Smallest possible now: 2 GB\n"}},
		{"resize_in_progress", `{"code":"resize_in_progress","error":"a disk resize is in progress; try again when it has finished"}`,
			http.StatusConflict, []string{"❌ Failed to resize database: a disk resize is in progress; try again when it has finished\n"}},
		{"database_not_running", `{"code":"database_not_running","error":"the database must be running to change its disk"}`,
			http.StatusConflict, []string{"❌ Failed to resize database: the database must be running to change its disk\n"}},
		{"storage_capacity", `{"code":"storage_capacity","error":"the platform does not have the storage for this disk right now"}`,
			http.StatusServiceUnavailable, []string{"❌ Failed to resize database: the platform does not have the storage for this disk right now\n"}},
		{"below 1 GB", `{"error":"a database disk is at least 1 GB"}`,
			http.StatusBadRequest, []string{"❌ Failed to resize database: a database disk is at least 1 GB\n"}},
		{"points budget", `{"error":"this change would exceed your plan's points budget; upgrade your plan"}`,
			http.StatusConflict, []string{"exceed your plan's points budget", "Switch to pay-as-you-go (PAYG)"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts, stub := newDBStub(t, pgRow("running", 10, ""), tc.status, tc.reply)
			cliHome(t, ts.URL)
			fastResizeWait(t, time.Second)

			out := runCLI(t, t.TempDir(), "db", "resize", "pg", "--disk-gb", "1")

			for _, want := range tc.want {
				if !strings.Contains(out, want) {
					t.Errorf("output = %q; want %q", out, want)
				}
			}
			if n := countRequests(stub.seen(), "GET /api/v1/databases/d1"); n != 0 {
				t.Errorf("polled %d times after a refusal", n)
			}
			if lastExitCode != 1 {
				t.Errorf("exit code = %d; want 1", lastExitCode)
			}
		})
	}
}

// TestDBResize_TierOnlyChangePrintsTheSummary: no disk change, no wait.
func TestDBResize_TierOnlyChangePrintsTheSummary(t *testing.T) {
	ts, stub := newDBStub(t, pgRow("running", 10, ""), http.StatusOK,
		strings.Replace(pgReply("running", 10, ""), `"tier_slug":"s"`, `"tier_slug":"m"`, 1))
	cliHome(t, ts.URL)
	fastResizeWait(t, time.Second)

	out := runCLI(t, t.TempDir(), "db", "resize", "pg", "--tier", "m")

	for _, want := range []string{"✅ Database 'pg' updated\n", "   Tier:    m\n", "   Disk:    10 GB\n", "   Backup:  weekly\n"} {
		if !strings.Contains(out, want) {
			t.Errorf("output = %q; want %q", out, want)
		}
	}
	if _, sent := stub.patchBody()["disk_gb"]; sent {
		t.Errorf("PATCH body = %v; a tier-only change sends no disk", stub.patchBody())
	}
	if n := countRequests(stub.seen(), "GET /api/v1/databases/d1"); n != 0 {
		t.Errorf("polled %d times without a disk change", n)
	}
}

func TestDBResize_DiskGBBelowOneIsRefusedLocally(t *testing.T) {
	ts, stub := newDBStub(t, pgRow("running", 10, ""), http.StatusOK, pgReply("running", 10, ""))
	cliHome(t, ts.URL)

	out := runCLI(t, t.TempDir(), "db", "resize", "pg", "--disk-gb", "0")

	if !strings.Contains(out, "--disk-gb must be a whole number of GB, at least 1") {
		t.Errorf("output = %q; want the local refusal", out)
	}
	if len(stub.seen()) != 0 {
		t.Errorf("requests = %v; want none", stub.seen())
	}
	if lastExitCode != 1 {
		t.Errorf("exit code = %d; want 1", lastExitCode)
	}
}

func TestDBListAndInfo_ShowTheDiskChange(t *testing.T) {
	resizing := `{"id":"d2","name":"shop","type":"mongodb","status":"resizing","disk_gb":10,"resize":{"direction":"shrink","target_disk_gb":3,"phase":"copy_back"}}`
	failed := `{"id":"d3","name":"crm","type":"postgres","status":"running","disk_gb":10,"resize":{"direction":"shrink","target_disk_gb":1,"error":"the move to 1 GB failed while stopping the database and was undone: timeout"}}`
	measured := strings.Replace(pgRow("running", 10, ""), `"disk_gb":10`, `"disk_gb":10,"disk_used_bytes":1288490188,"min_disk_gb":2`, 1)
	ts, _ := newDBStub(t, measured+","+resizing+","+failed, http.StatusOK, "")
	cliHome(t, ts.URL)

	out := runCLI(t, t.TempDir(), "db", "list")
	for _, want := range []string{
		fmt.Sprintf("   %-15s  %-10s  %s\n", "pg", "postgres", "running"),
		fmt.Sprintf("   %-15s  %-10s  %s\n", "shop", "mongodb", "resizing to 3 GB · copying the data onto the new disk"),
		fmt.Sprintf("   %-15s  %-10s  %s\n", "crm", "postgres", "running · disk change to 1 GB failed: the move to 1 GB failed while stopping the database and was undone: timeout"),
	} {
		if !strings.Contains(out, want) {
			t.Errorf("db list = %q; want %q", out, want)
		}
	}

	out = runCLI(t, t.TempDir(), "db", "info", "shop")
	if !strings.Contains(out, "   Status:     resizing to 3 GB · copying the data onto the new disk\n") {
		t.Errorf("db info = %q; want the change in the status line", out)
	}

	out = runCLI(t, t.TempDir(), "db", "info", "pg")
	if !strings.Contains(out, "   Disk used:  1.2 GB (smallest disk now: 2 GB)\n") {
		t.Errorf("db info = %q; want what the disk holds and the smallest size", out)
	}
}

func TestDBResizeWiring(t *testing.T) {
	c, _, err := rootCmd.Find([]string{"db", "resize"})
	if err != nil || c.Name() != "resize" {
		t.Fatalf("Find(db resize) = %v, %v", c, err)
	}
	if c.Flags().Lookup("no-wait") == nil {
		t.Error("db resize should expose --no-wait")
	}
	help := c.Short + c.Long + c.Flags().Lookup("disk-gb").Usage
	if strings.Contains(help, "grow-only") || !strings.Contains(help, "shrink") {
		t.Errorf("help = %q; the disk shrinks now", help)
	}
}
