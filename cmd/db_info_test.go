package cmd

import (
	"strings"
	"testing"
)

// No shared credentials, part 4: the database's own login leaves every API
// response, and `db info` shows no username line even against a backend that
// still sends one.
func TestDBInfo_ShowsNoUsername(t *testing.T) {
	ts, _ := detailsStub(t, "/api/v1/databases", `{"databases":[`+shopDB+`]}`, `{"connections":[]}`)
	cliHome(t, ts.URL)

	out := runCLI(t, t.TempDir(), "db", "info", "shop")

	if !strings.Contains(out, "   Database:   shop\n") {
		t.Fatalf("output = %s; want the database line", out)
	}
	for _, never := range []string{"Username", "u_3f1a2b3c"} {
		if strings.Contains(out, never) {
			t.Errorf("db info must not show %q:\n%s", never, out)
		}
	}
}
