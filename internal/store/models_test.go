package store

import "testing"

func TestTeamAllowsModel(t *testing.T) {
	tests := []struct {
		name    string
		allowed []string
		alias   string
		want    bool
	}{
		{name: "empty defaults allow", allowed: nil, alias: "gpt-4", want: true},
		{name: "wildcard allows", allowed: []string{"*"}, alias: "gpt-4", want: true},
		{name: "exact match allows", allowed: []string{"gpt-4"}, alias: "gpt-4", want: true},
		{name: "non-match denies", allowed: []string{"claude"}, alias: "gpt-4", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			team := &Team{AllowedModels: tc.allowed}
			if got := team.AllowsModel(tc.alias); got != tc.want {
				t.Fatalf("AllowsModel(%q) = %v, want %v", tc.alias, got, tc.want)
			}
		})
	}
}

func TestVirtualKeyAllowsModel(t *testing.T) {
	tests := []struct {
		name    string
		allowed []string
		alias   string
		want    bool
	}{
		{name: "empty defaults allow", allowed: nil, alias: "gpt-4", want: true},
		{name: "wildcard allows", allowed: []string{"*"}, alias: "gpt-4", want: true},
		{name: "exact match allows", allowed: []string{"claude"}, alias: "claude", want: true},
		{name: "non-match denies", allowed: []string{"claude"}, alias: "gpt-4", want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			key := &VirtualKey{AllowedModels: tc.allowed}
			if got := key.AllowsModel(tc.alias); got != tc.want {
				t.Fatalf("AllowsModel(%q) = %v, want %v", tc.alias, got, tc.want)
			}
		})
	}
}
