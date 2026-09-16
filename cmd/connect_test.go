package cmd

import (
	"strings"
	"testing"

	"paas-cli/internal/api"
)

func TestPlanConnect_NewResourceDefaultsLevelToTheServer(t *testing.T) {
	plan, err := planConnect(siteView(), "database", "pg-analytics", "")
	if err != nil {
		t.Fatalf("planConnect: %v", err)
	}
	if plan.NoChange || plan.Held != nil || plan.Item.Kind != "database" || plan.Item.ResourceID != "d2" || plan.Item.Level != "" {
		t.Errorf("plan = %+v; want a POST for d2 with no level", plan)
	}
	if plan.Note != "" {
		t.Errorf("no admin note for a database, got %q", plan.Note)
	}
}

func TestPlanConnect_AlreadyConnectedSameLevelIsNoChange(t *testing.T) {
	plan, err := planConnect(siteView(), "auth_app", "shop", "")
	if err != nil || !plan.NoChange || plan.Held == nil || plan.Held.Level != "client" {
		t.Errorf("plan = %+v, %v; want NoChange at client", plan, err)
	}
	plan, err = planConnect(siteView(), "auth_app", "shop", "client")
	if err != nil || !plan.NoChange {
		t.Errorf("explicit same level is still NoChange, got %+v, %v", plan, err)
	}
}

func TestPlanConnect_LevelChangeCarriesTheAdminNote(t *testing.T) {
	plan, err := planConnect(siteView(), "auth_app", "shop", "admin")
	if err != nil {
		t.Fatalf("planConnect: %v", err)
	}
	if plan.NoChange || plan.Item.Level != "admin" || plan.Item.ResourceID != "a1" || plan.Held == nil {
		t.Errorf("plan = %+v; want a level change POST for a1", plan)
	}
	if plan.Note != adminNote {
		t.Errorf("note = %q; want the admin note", plan.Note)
	}
}

func TestPlanConnect_RejectsBadLevelBeforeTheRequest(t *testing.T) {
	_, err := planConnect(siteView(), "bucket", "uploads", "admin")
	if err == nil || !strings.Contains(err.Error(), "read-write") {
		t.Errorf("want an accepted-levels error, got %v", err)
	}
}

func TestPlanConnect_UnknownName(t *testing.T) {
	_, err := planConnect(siteView(), "database", "nope", "")
	if err == nil || !strings.Contains(err.Error(), "no database named") {
		t.Errorf("got %v", err)
	}
}

func TestDisconnectPrompt_YesSkipsAndAnswersGate(t *testing.T) {
	if !confirmDisconnect("database", "pg", "main", nil, true, func() string { t.Fatal("must not prompt with --yes"); return "" }) {
		t.Error("--yes must confirm")
	}
	captureStdout(t, func() {
		if confirmDisconnect("database", "pg", "main", nil, false, func() string { return "n" }) {
			t.Error("'n' must cancel")
		}
		if !confirmDisconnect("database", "pg", "main", nil, false, func() string { return "Y" }) {
			t.Error("'Y' must confirm")
		}
	})
}

func TestDisconnectPrompt_NamesTheVariablesThatLeave(t *testing.T) {
	out := captureStdout(t, func() {
		confirmDisconnect("database", "pg", "main", []string{"DATABASE_URL", "DATABASE_URL_PG"}, false, func() string { return "n" })
	})
	if !strings.Contains(out, "DATABASE_URL, DATABASE_URL_PG leave the app") || !strings.Contains(out, "network path closes") {
		t.Errorf("prompt = %q; want the names and the database network path", out)
	}
	out = captureStdout(t, func() {
		confirmDisconnect("bucket", "uploads", "main", []string{"STORAGE_BUCKET"}, false, func() string { return "n" })
	})
	if !strings.Contains(out, "STORAGE_BUCKET leave the app.") || strings.Contains(out, "network path") {
		t.Errorf("a bucket prompt must not mention the network path, got %q", out)
	}
	out = captureStdout(t, func() {
		confirmDisconnect("database", "pg", "main", nil, false, func() string { return "n" })
	})
	if !strings.Contains(out, "its variables leave the app") {
		t.Errorf("without names today's sentence stays, got %q", out)
	}
}

func TestConnectOutcomeLines(t *testing.T) {
	row := &api.Connection{Kind: "database", ResourceName: "pg", Level: "connect", SiteSlug: "main"}
	if got := connectedLine(row, nil); !strings.Contains(got, "Connected database 'pg' to 'main'") || !strings.Contains(got, "connect") {
		t.Errorf("new connection line = %q", got)
	}
	held := &api.Connection{Kind: "auth_app", ResourceName: "shop", Level: "client", SiteSlug: "main"}
	row = &api.Connection{Kind: "auth_app", ResourceName: "shop", Level: "admin", SiteSlug: "main"}
	if got := connectedLine(row, held); !strings.Contains(got, "client → admin") {
		t.Errorf("level change line = %q", got)
	}
}

func TestInjectsLine(t *testing.T) {
	got := injectsLine("main", []string{"DATABASE_URL", "DATABASE_URL_PG"})
	if !strings.Contains(got, "The platform now injects into 'main': DATABASE_URL, DATABASE_URL_PG") {
		t.Errorf("injects line = %q", got)
	}
	if got := injectsLine("main", nil); !strings.Contains(got, "No variables yet") {
		t.Errorf("a connection with no names yet = %q", got)
	}
}
