package cmd

import (
	"testing"

	"paas-cli/internal/api"
)

func TestAppliedLine(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		site api.Site
		want string
	}{
		{api.Site{}, ""},
		{api.Site{Applied: &yes}, "   The app is moving to the new size now, with no downtime."},
		{api.Site{Applied: &no, ApplyNote: "the site has not been deployed yet; its first deploy uses the new size"}, "   The site has not been deployed yet; its first deploy uses the new size."},
		{api.Site{Applied: &no}, "   It applies at the next deploy."},
	} {
		if got := appliedLine(&tc.site); got != tc.want {
			t.Errorf("appliedLine(%+v) = %q; want %q", tc.site, got, tc.want)
		}
	}
}
