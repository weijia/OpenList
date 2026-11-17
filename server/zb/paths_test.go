package zb

import "testing"

func TestNormalizeVirtualAcceptsWindowsDrivePaths(t *testing.T) {
	cases := map[string]string{
		`C:\codes`:      `/C/codes`,
		`C:/codes`:      `/C/codes`,
		`c:\codes\_tmp`: `/C/codes/_tmp`,
		`/C/codes`:      `/C/codes`,
		`/C:/codes`:     `/C/codes`,
		`/`:             `/`,
	}
	for input, want := range cases {
		if got := normalizeVirtual(input); got != want {
			t.Errorf("normalizeVirtual(%q) = %q, want %q", input, got, want)
		}
	}
}
