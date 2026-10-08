package hostfs

import "testing"

func TestWindowsNormalizePickedPaths(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{"  \"C:\\workspace with space\\ação\\\"\r\n", `C:\workspace with space\ação`},
		{"C:\\\r\n", `C:\`},
		{"\\\\server\\share\\project\\\r\n", `\\server\share\project`},
		{"\r\n", ""},
	} {
		if got := normalizePicked(tc.raw); got != tc.want {
			t.Errorf("normalize %q: %q want %q", tc.raw, got, tc.want)
		}
	}
}
