package antigravity

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testServer(t *testing.T, quotaBody string) *Provider {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/tier" {
			fmt.Fprint(w, `{"currentTier":{"id":"google-ai-pro","name":"Pro"}}`)
			return
		}
		fmt.Fprint(w, quotaBody)
	}))
	t.Cleanup(srv.Close)
	p, err := NewProvider(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p.quotaEndpoints = []string{srv.URL + "/quota"}
	p.tierEndpoints = []string{srv.URL + "/tier"}
	p.httpClient = srv.Client()
	return p
}

func TestFetchQuotasCanonicalOrderRegardlessOfAPIOrder(t *testing.T) {
	weeklyFirst := `{"groups":[{"displayName":"Gemini Models","buckets":[{"bucketId":"gemini-weekly","window":"weekly","resetTime":"2030-08-29T12:08:59Z","remainingFraction":0.583},{"bucketId":"gemini-5h","window":"5h","resetTime":"2030-08-27T15:45:28Z","remainingFraction":0.853}]}]}`
	fiveFirst := `{"groups":[{"displayName":"Gemini Models","buckets":[{"bucketId":"gemini-5h","window":"5h","resetTime":"2030-08-27T15:45:28Z","remainingFraction":0.853},{"bucketId":"gemini-weekly","window":"weekly","resetTime":"2030-08-29T12:08:59Z","remainingFraction":0.583}]}]}`
	ctx := context.Background()

	q1, err := testServer(t, weeklyFirst).fetchQuotas(ctx, "tok")
	if err != nil {
		t.Fatal(err)
	}
	q2, err := testServer(t, fiveFirst).fetchQuotas(ctx, "tok")
	if err != nil {
		t.Fatal(err)
	}
	if len(q1) != 2 || len(q2) != 2 {
		t.Fatalf("expected 2 quotas, got %d and %d", len(q1), len(q2))
	}
	for i := range q1 {
		if q1[i].Label != q2[i].Label || q1[i].PercentLeft != q2[i].PercentLeft {
			t.Fatalf("order-dependent display: %v vs %v", q1, q2)
		}
	}
	if q1[0].Label != "5-hour" || q1[1].Label != "Weekly" {
		t.Fatalf("expected [5-hour Weekly], got [%s %s]", q1[0].Label, q1[1].Label)
	}
}

func TestFetchQuotasIgnoresGroupOrder(t *testing.T) {
	// 3p group first — must still show the Gemini pool, not Claude/GPT.
	body := `{"groups":[{"displayName":"Claude and GPT models","buckets":[{"bucketId":"3p-5h","window":"5h","remainingFraction":0.1},{"bucketId":"3p-weekly","window":"weekly","resetTime":"2030-08-31T11:50:43Z","remainingFraction":0.2}]},{"displayName":"Gemini Models","buckets":[{"bucketId":"gemini-5h","window":"5h","resetTime":"2030-08-27T15:45:28Z","remainingFraction":0.853},{"bucketId":"gemini-weekly","window":"weekly","resetTime":"2030-08-29T12:08:59Z","remainingFraction":0.583}]}]}`
	q, err := testServer(t, body).fetchQuotas(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if len(q) != 2 {
		t.Fatalf("expected 2 gemini quotas, got %d: %v", len(q), q)
	}
	if q[0].PercentLeft != 85.3 || q[1].PercentLeft != 58.3 {
		t.Fatalf("wrong pool selected (3p leak?): %v", q)
	}
}

func TestFetchQuotasMissingResetTimeShowsDash(t *testing.T) {
	// Full bucket with no reset scheduled (live API shape) must not say "now".
	body := `{"groups":[{"displayName":"Gemini Models","buckets":[{"bucketId":"gemini-weekly","window":"weekly","resetTime":"2030-08-29T12:08:59Z","remainingFraction":0.886},{"bucketId":"gemini-5h","window":"5h","remainingFraction":1.0}]}]}`
	q, err := testServer(t, body).fetchQuotas(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if len(q) != 2 {
		t.Fatalf("expected 2 quotas, got %d", len(q))
	}
	if q[0].ResetIn != "—" {
		t.Fatalf("missing resetTime should render '—', got %q", q[0].ResetIn)
	}
	if !q[1].ResetAt.After(time.Now()) {
		t.Fatalf("weekly reset should parse to future time, got %v", q[1].ResetAt)
	}
}

func TestParseResetTime(t *testing.T) {
	now := time.Now()
	if _, s := parseResetTime("", now); s != "—" {
		t.Fatalf("empty resetTime: got %q, want —", s)
	}
	if _, s := parseResetTime("not-a-time", now); s != "—" {
		t.Fatalf("garbage resetTime: got %q, want —", s)
	}
	rt, s := parseResetTime("2030-01-01T00:00:00Z", now)
	if !rt.After(now) || s == "now" || s == "—" {
		t.Fatalf("future resetTime misparsed: %v %q", rt, s)
	}
	if _, s := parseResetTime("2020-01-01T00:00:00Z", now); s != "now" {
		t.Fatalf("past resetTime: got %q, want now", s)
	}
}

func TestFetchQuotasFallsBackToNextEndpoint(t *testing.T) {
	goodBody := `{"groups":[{"displayName":"Gemini Models","buckets":[{"bucketId":"gemini-5h","window":"5h","resetTime":"2030-08-27T15:45:28Z","remainingFraction":0.84},{"bucketId":"gemini-weekly","window":"weekly","resetTime":"2030-08-29T12:08:59Z","remainingFraction":0.85}]}]}`
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, goodBody)
	}))
	t.Cleanup(good.Close)
	// Dead endpoint first: connection refused forces fallback.
	p, err := NewProvider(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p.quotaEndpoints = []string{"http://127.0.0.1:1/quota", good.URL}
	p.httpClient = good.Client()
	q, err := p.fetchQuotas(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if len(q) != 2 || q[0].Label != "5-hour" || q[0].PercentLeft != 84 {
		t.Fatalf("fallback did not yield good endpoint data: %+v", q)
	}
}

func TestFetchQuotasAllEndpointsFail(t *testing.T) {
	p, err := NewProvider(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p.quotaEndpoints = []string{"http://127.0.0.1:1/quota"}
	if _, err := p.fetchQuotas(context.Background(), "tok"); err == nil {
		t.Fatal("expected error when all endpoints fail")
	}
}

func TestFetchPlanTierFallsBack(t *testing.T) {
	p, err := NewProvider(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p.tierEndpoints = []string{"http://127.0.0.1:1/tier"}
	if got := p.fetchPlanTier(context.Background(), "tok"); got != "Standard" {
		t.Fatalf("all-fail tier should be Standard, got %q", got)
	}
}
