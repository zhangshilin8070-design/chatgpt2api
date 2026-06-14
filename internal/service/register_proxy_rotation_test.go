package service

import (
	"reflect"
	"strings"
	"testing"
)

func TestNormalizeRegisterProxiesCleansAndDeduplicates(t *testing.T) {
	got := normalizeRegisterProxies("\n socks5://a.example:3000 \n socks5://b.example:3000\n socks5://a.example:3000 \n")
	want := []string{"socks5://a.example:3000", "socks5://b.example:3000"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalizeRegisterProxies() = %#v, want %#v", got, want)
	}
}

func TestRegisterProxyForTaskRoundRobin(t *testing.T) {
	config := map[string]any{"proxies": []string{"socks5://a", "socks5://b", "socks5://c"}, "proxy": "socks5://fallback"}
	cases := []struct {
		index int
		want  string
		pos   int
	}{
		{1, "socks5://a", 1},
		{2, "socks5://b", 2},
		{3, "socks5://c", 3},
		{4, "socks5://a", 1},
	}
	for _, tc := range cases {
		got, pos, total := registerProxyForTask(config, tc.index)
		if got != tc.want || pos != tc.pos || total != 3 {
			t.Fatalf("task %d proxy = (%q,%d,%d), want (%q,%d,3)", tc.index, got, pos, total, tc.want, tc.pos)
		}
	}
}

func TestRegisterProxyForTaskFallsBackToSingleProxy(t *testing.T) {
	got, pos, total := registerProxyForTask(map[string]any{"proxy": "socks5://single"}, 1)
	if got != "socks5://single" || pos != 0 || total != 0 {
		t.Fatalf("fallback proxy = (%q,%d,%d)", got, pos, total)
	}
}

func TestMaskRegisterProxyHidesCredentials(t *testing.T) {
	got := maskRegisterProxy("socks5://user:secret@example.com:3000")
	if strings.Contains(got, "user") || strings.Contains(got, "secret") {
		t.Fatalf("maskRegisterProxy leaked credentials: %q", got)
	}
	if !strings.Contains(got, "example.com:3000") {
		t.Fatalf("maskRegisterProxy lost host: %q", got)
	}
}
