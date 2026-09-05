package main

import "testing"

func TestBindsLoopback(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{":8674", false},
		{"0.0.0.0:8674", false},
		{"192.168.1.10:8674", false},
		{"[::]:8674", false},
		{"127.0.0.1:8674", true},
		{"[::1]:8674", true},
		{"localhost:8674", true},
		{"127.0.0.1", true},
		{"::1", true},
	}
	for _, c := range cases {
		if got := bindsLoopback(c.addr); got != c.want {
			t.Errorf("bindsLoopback(%q) = %v, want %v", c.addr, got, c.want)
		}
	}
}
