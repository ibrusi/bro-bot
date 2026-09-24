package claude

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestParseClaudeCLIQuotaRealOutput(t *testing.T) {
	now := time.Date(2026, 9, 24, 7, 10, 0, 0, time.UTC)
	raw := []byte(`{"is_error":false,"subtype":"success","result":"You are currently using your subscription to power your Claude Code usage\n\nCurrent session: 0% used · resets Sep 24, 11:10am (UTC)\nCurrent week (all models): 30% used · resets Sep 27, 12am (UTC)\n\nWhat's contributing to your limits usage?\nApproximate, based on local sessions on this machine — does not include other devices or claude.ai.\n\nLast 7d · 127 requests · 8 sessions\n  27% of your usage was at >150k context\n  Top MCP servers: bro_bot 2%","local_command":"usage","type":"result"}`)

	parsed, ok := parseClaudeCLIQuota(raw, "ru", now)
	if !ok {
		t.Fatal("parseClaudeCLIQuota вернул false на реальном выводе Claude Code")
	}

	var resp struct {
		Status  string `json:"status"`
		Result  string `json:"result"`
		Command struct {
			Name string `json:"name"`
			Data struct {
				Groups []struct {
					Name    string `json:"name"`
					Buckets []struct {
						ID                string   `json:"id"`
						Name              string   `json:"name"`
						Window            string   `json:"window"`
						RemainingFraction *float64 `json:"remaining_fraction"`
						ResetTime         string   `json:"reset_time"`
					} `json:"buckets"`
				} `json:"groups"`
			} `json:"data"`
		} `json:"command"`
	}

	if err := json.Unmarshal(parsed, &resp); err != nil {
		t.Fatalf("unmarshal parsed quota: %v", err)
	}

	if resp.Status != "SUCCESS" {
		t.Errorf("status = %q, want SUCCESS", resp.Status)
	}
	if len(resp.Command.Data.Groups) != 1 {
		t.Fatalf("groups count = %d, want 1", len(resp.Command.Data.Groups))
	}

	group := resp.Command.Data.Groups[0]
	if group.Name != "Подписка Claude" {
		t.Errorf("group name = %q, want 'Подписка Claude'", group.Name)
	}

	if len(group.Buckets) != 2 {
		t.Fatalf("buckets count = %d, want 2", len(group.Buckets))
	}

	// Bucket 1: Current session
	b1 := group.Buckets[0]
	if b1.Name != "Current session" {
		t.Errorf("bucket 0 name = %q, want 'Current session'", b1.Name)
	}
	if b1.Window != "5h" {
		t.Errorf("bucket 0 window = %q, want '5h'", b1.Window)
	}
	if b1.RemainingFraction == nil || *b1.RemainingFraction != 1.0 {
		t.Errorf("bucket 0 fraction = %v, want 1.0", b1.RemainingFraction)
	}
	if b1.ResetTime != "2026-09-24T11:10:00Z" {
		t.Errorf("bucket 0 reset_time = %q, want '2026-09-24T11:10:00Z'", b1.ResetTime)
	}

	// Bucket 2: Current week (all models)
	b2 := group.Buckets[1]
	if b2.Name != "Current week (all models)" {
		t.Errorf("bucket 1 name = %q, want 'Current week (all models)'", b2.Name)
	}
	if b2.Window != "weekly" {
		t.Errorf("bucket 1 window = %q, want 'weekly'", b2.Window)
	}
	if b2.RemainingFraction == nil || *b2.RemainingFraction != 0.7 {
		t.Errorf("bucket 1 fraction = %v, want 0.7", b2.RemainingFraction)
	}
	if b2.ResetTime != "2026-09-27T00:00:00Z" {
		t.Errorf("bucket 1 reset_time = %q, want '2026-09-27T00:00:00Z'", b2.ResetTime)
	}
}

func TestParseClaudeCLIQuotaEnglishGroup(t *testing.T) {
	now := time.Date(2026, 9, 24, 7, 10, 0, 0, time.UTC)
	raw := []byte("Current session: 10% used · resets Sep 24, 11:10am (UTC)")

	parsed, ok := parseClaudeCLIQuota(raw, "en", now)
	if !ok {
		t.Fatal("expected ok = true")
	}

	var resp struct {
		Command struct {
			Data struct {
				Groups []struct {
					Name string `json:"name"`
				} `json:"groups"`
			} `json:"data"`
		} `json:"command"`
	}
	_ = json.Unmarshal(parsed, &resp)
	if len(resp.Command.Data.Groups) != 1 || resp.Command.Data.Groups[0].Name != "Claude Subscription" {
		t.Errorf("expected group name 'Claude Subscription', got %v", resp.Command.Data.Groups)
	}
}

func TestParseClaudeCLIQuotaMultipleBucketsAndLimitReached(t *testing.T) {
	now := time.Date(2026, 9, 24, 7, 10, 0, 0, time.UTC)
	text := `Current session: 100% used (limit reached) · resets Sep 24, 11:10am (UTC)
Current week (all models): 45% used · resets Sep 27, 12am (UTC)
Current week (Sonnet only): 5% used · resets Sep 27, 12am (UTC)
Spend limit: 20% used · resets Oct 1, 12am (UTC)`

	parsed, ok := parseClaudeCLIQuota([]byte(text), "ru", now)
	if !ok {
		t.Fatal("expected ok = true")
	}

	var resp struct {
		Command struct {
			Data struct {
				Groups []struct {
					Buckets []struct {
						ID                string   `json:"id"`
						Name              string   `json:"name"`
						Window            string   `json:"window"`
						RemainingFraction *float64 `json:"remaining_fraction"`
						ResetTime         string   `json:"reset_time"`
					} `json:"buckets"`
				} `json:"groups"`
			} `json:"data"`
		} `json:"command"`
	}
	_ = json.Unmarshal(parsed, &resp)
	buckets := resp.Command.Data.Groups[0].Buckets
	if len(buckets) != 4 {
		t.Fatalf("expected 4 buckets, got %d", len(buckets))
	}

	// 100% used -> fraction = 0.0
	if *buckets[0].RemainingFraction != 0.0 {
		t.Errorf("bucket 0 fraction = %v, want 0.0", *buckets[0].RemainingFraction)
	}
	// Sonnet only -> weekly window
	if buckets[2].Window != "weekly" || buckets[2].ID != "claude-weekly-sonnet" {
		t.Errorf("bucket 2 = %+v, want weekly / claude-weekly-sonnet", buckets[2])
	}
	if *buckets[2].RemainingFraction != 0.95 {
		t.Errorf("bucket 2 fraction = %v, want 0.95", *buckets[2].RemainingFraction)
	}
	// Spend limit
	if buckets[3].ResetTime != "2026-10-01T00:00:00Z" {
		t.Errorf("bucket 3 reset_time = %q, want '2026-10-01T00:00:00Z'", buckets[3].ResetTime)
	}
}

func TestParseClaudeCLIQuotaNonMatchingInput(t *testing.T) {
	now := time.Now()
	cases := [][]byte{
		[]byte(""),
		[]byte("Total cost: $0.0000\nUsage: 0 input, 0 output"),
		[]byte(`{"type":"result","result":"Total cost: $0.0000\nUsage: 0 input, 0 output"}`),
		[]byte("Some random error from claude"),
	}

	for _, tc := range cases {
		_, ok := parseClaudeCLIQuota(tc, "ru", now)
		if ok {
			t.Errorf("expected ok = false for %q", string(tc))
		}
	}
}

func TestParseClaudeResetTimeFormats(t *testing.T) {
	now := time.Date(2026, 9, 24, 7, 0, 0, 0, time.UTC)

	cases := []struct {
		input string
		want  string
	}{
		{"Sep 24, 11:10am (UTC)", "2026-09-24T11:10:00Z"},
		{"Sep 27, 12am (UTC)", "2026-09-27T00:00:00Z"},
		{"Sep 24, 12pm (UTC)", "2026-09-24T12:00:00Z"},
		{"Sep 24, 1:05pm (UTC)", "2026-09-24T13:05:00Z"},
		{"11:10am (UTC)", "2026-09-24T11:10:00Z"},
		{"Oct 1, 2026, 3:00pm (UTC)", "2026-10-01T15:00:00Z"},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			got, err := parseClaudeResetTime(tc.input, now)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Format(time.RFC3339) != tc.want {
				t.Errorf("parseClaudeResetTime(%q) = %q, want %q", tc.input, got.Format(time.RFC3339), tc.want)
			}
		})
	}
}

func TestLiveClaudeAdapterGetQuota(t *testing.T) {
	adapter := NewClaudeAdapter()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	out, err := adapter.GetQuota(ctx, "ru")
	if err != nil {
		t.Skipf("claude CLI not working or not authenticated: %v", err)
	}

	var resp struct {
		Status  string `json:"status"`
		Command struct {
			Data struct {
				Groups []struct {
					Name    string `json:"name"`
					Buckets []struct {
						Name              string   `json:"name"`
						RemainingFraction *float64 `json:"remaining_fraction"`
						ResetTime         string   `json:"reset_time"`
					} `json:"buckets"`
				} `json:"groups"`
			} `json:"data"`
		} `json:"command"`
	}

	if err := json.Unmarshal(out, &resp); err != nil {
		t.Fatalf("unmarshal error: %v, raw output: %s", err, string(out))
	}

	if resp.Status != "SUCCESS" {
		t.Errorf("status = %q, want SUCCESS", resp.Status)
	}
	if len(resp.Command.Data.Groups) == 0 {
		t.Fatalf("expected at least 1 group, got 0. Raw: %s", string(out))
	}
	group := resp.Command.Data.Groups[0]
	t.Logf("Live quota group: %q, buckets count: %d", group.Name, len(group.Buckets))
	for _, b := range group.Buckets {
		t.Logf("  Bucket: name=%q fraction=%v reset=%q", b.Name, *b.RemainingFraction, b.ResetTime)
	}
}

