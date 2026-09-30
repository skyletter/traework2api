package scheduler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"trae2api-web/internal/auth"
	"trae2api-web/internal/pool"
	"trae2api-web/internal/upstream"
)

func TestNextFire(t *testing.T) {
	loc := time.Local
	now := time.Date(2026, 7, 27, 10, 0, 0, 0, loc)
	next := nextFire(now, []int{9, 21})
	if next.Hour() != 21 || next.Day() != 27 {
		t.Errorf("next=%v want 21:00 same day", next)
	}
	now = time.Date(2026, 7, 27, 22, 0, 0, 0, loc)
	next = nextFire(now, []int{9, 21})
	if next.Hour() != 9 || next.Day() != 28 {
		t.Errorf("next=%v want 09:00 next day", next)
	}
	now = time.Date(2026, 7, 27, 9, 0, 0, 0, loc)
	next = nextFire(now, []int{9})
	if next.Day() != 28 {
		t.Errorf("exact match should roll to next day: %v", next)
	}
}

func TestNextFireMergesSchedules(t *testing.T) {
	now := time.Date(2026, 7, 27, 20, 0, 0, 0, time.Local)
	next := nextFire(now, []int{9, 21, 22})
	if next.Hour() != 21 {
		t.Errorf("next=%v want 21 (earliest of 21/22)", next)
	}
}

// fakeUpstream 同时模拟 checkin/ent_usage/refresh。
type fakeUpstream struct {
	checkinCalls   atomic.Int32
	claimCalls     atomic.Int32
	refreshCalls   atomic.Int32
	resourceRemain int64
	softRateClaim  atomic.Bool
}

func (f *fakeUpstream) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/checkin_credits/status"):
			f.checkinCalls.Add(1)
			w.Write([]byte(`{"checked_in":false,"credits":200,"enable":true}`))
		case strings.HasSuffix(r.URL.Path, "/checkin_credits/claim"):
			f.claimCalls.Add(1)
			if f.softRateClaim.Load() {
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write([]byte(`{"code":3004,"message":"Too Many Requests"}`))
				return
			}
			w.Write([]byte(`{"code":0,"message":"success"}`))
		case strings.HasSuffix(r.URL.Path, "/ide_user_ent_usage"):
			w.Write([]byte(`{"is_credits_billing":true,"user_entitlement_pack_list":[{"entitlement_base_info":{"quota":{"credits_limit":` +
				jsonI64(f.resourceRemain) + `}}}]}`))
		case strings.HasSuffix(r.URL.Path, "/ExchangeToken"):
			f.refreshCalls.Add(1)
			w.Write([]byte(`{"Result":{"Token":"newat","RefreshToken":"newrt","TokenExpireAt":1786805537,"TokenExpireDuration":1209600}}`))
		default:
			http.Error(w, "not found: "+r.URL.Path, 404)
		}
	}))
}

func jsonI64(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func newTestScheduler(f *fakeUpstream, p *pool.Pool, srv *httptest.Server) *Scheduler {
	up := &upstream.Client{
		HTTP:      srv.Client(),
		AgentHost: srv.URL,
		UgHost:    srv.URL,
		OAuthHost: srv.URL,
		ClientID:  upstream.ClientID,
	}
	return New(Config{
		Pool:         p,
		Upstream:     up,
		CheckinHour:  9,
		RefreshHours: []int{3},
		RefreshSkew:  time.Hour,
	})
}

func TestRunCheckinReenablesCoolingAccount(t *testing.T) {
	f := &fakeUpstream{resourceRemain: 500}
	srv := f.server()
	defer srv.Close()

	p := pool.New("")
	a := &auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999}
	p.Add(a)
	p.Cooldown("u1", pool.CoolPlan, time.Hour, "plan limit")

	s := newTestScheduler(f, p, srv)
	s.RunCheckinNow()
	if f.checkinCalls.Load() != 1 {
		t.Errorf("checkin status calls=%d", f.checkinCalls.Load())
	}
	if f.claimCalls.Load() != 1 {
		t.Errorf("claim calls=%d", f.claimCalls.Load())
	}
	st, _ := p.Status("u1")
	if st.Cooling {
		t.Errorf("account should be reenabled after checkin with credits: %+v", st)
	}
	if st.Credits != 500 {
		t.Errorf("credits=%d want 500", st.Credits)
	}
}

func TestRunCheckinSkipsDisabled(t *testing.T) {
	f := &fakeUpstream{}
	srv := f.server()
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.Disable("u1", "session dead")

	s := newTestScheduler(f, p, srv)
	s.RunCheckinNow()
	if f.checkinCalls.Load() != 0 {
		t.Errorf("disabled account should be skipped, calls=%d", f.checkinCalls.Load())
	}
}

func TestRunRefreshRefreshesTokens(t *testing.T) {
	f := &fakeUpstream{}
	srv := f.server()
	defer srv.Close()

	p := pool.New("")
	a := &auth.Auth{UID: "u1", AccessToken: "old", RefreshToken: "rt", ExpiresAt: 1, ApiHost: srv.URL}
	p.Add(a)

	s := newTestScheduler(f, p, srv)
	s.RunRefreshNow()
	if f.refreshCalls.Load() != 1 {
		t.Errorf("refresh calls=%d", f.refreshCalls.Load())
	}
	if a.AccessToken != "newat" {
		t.Errorf("token not updated: %s", a.AccessToken)
	}
}

func TestRunRefreshSkipsFreshToken(t *testing.T) {
	f := &fakeUpstream{}
	srv := f.server()
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "fresh", RefreshToken: "rt", ExpiresAt: 9999999999, ApiHost: srv.URL})

	s := newTestScheduler(f, p, srv)
	s.RunRefreshNow()
	if f.refreshCalls.Load() != 0 {
		t.Errorf("fresh token should not refresh, calls=%d", f.refreshCalls.Load())
	}
}

func TestRunRefreshSessionDeadDisables(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		w.Write([]byte(`{"code":20101,"msg":"login required"}`))
	}))
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "old", RefreshToken: "rt", ExpiresAt: 1, ApiHost: srv.URL})

	up := &upstream.Client{HTTP: srv.Client(), AgentHost: srv.URL, UgHost: srv.URL, OAuthHost: srv.URL, ClientID: upstream.ClientID}
	s := New(Config{Pool: p, Upstream: up})
	s.RunRefreshNow()
	st, _ := p.Status("u1")
	if !st.Disabled {
		t.Errorf("should disable session-dead account: %+v", st)
	}
}

func TestRunCheckinRotatesDeviceOnRateLimit(t *testing.T) {
	f := &fakeUpstream{resourceRemain: 500}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/checkin_credits/status"):
			f.checkinCalls.Add(1)
			w.Write([]byte(`{"checked_in":false,"credits":200,"enable":true}`))
		case strings.HasSuffix(r.URL.Path, "/checkin_credits/claim"):
			f.claimCalls.Add(1)
			w.Write([]byte(`{"code":9074,"message":"too many users"}`))
		case strings.HasSuffix(r.URL.Path, "/ide_user_ent_usage"):
			w.Write([]byte(`{"is_credits_billing":true,"user_entitlement_pack_list":[{"entitlement_base_info":{"quota":{"credits_limit":500}}}]}`))
		default:
			http.Error(w, "not found", 404)
		}
	}))
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	s := newTestScheduler(f, p, srv)
	s.RunCheckinNow()
	if f.claimCalls.Load() != 1 {
		t.Fatalf("claim calls=%d want exactly 1 (no immediate retry on 9074)", f.claimCalls.Load())
	}
	if g := p.CheckinGeneration("u1"); g != 1 {
		t.Errorf("generation=%d want 1 after 9074 rotation", g)
	}
}

func TestRunCheckinKeepsGenerationWhenCheckedIn(t *testing.T) {
	f := &fakeUpstream{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/checkin_credits/status"):
			f.checkinCalls.Add(1)
			w.Write([]byte(`{"checked_in":true,"credits":200,"enable":true}`))
		case strings.HasSuffix(r.URL.Path, "/ide_user_ent_usage"):
			w.Write([]byte(`{"is_credits_billing":true,"user_entitlement_pack_list":[{"entitlement_base_info":{"quota":{"credits_limit":500}}}]}`))
		default:
			http.Error(w, "not found", 404)
		}
	}))
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	p.BumpCheckinGeneration("u1")
	p.BumpCheckinGeneration("u1")
	s := newTestScheduler(f, p, srv)
	s.RunCheckinNow()
	if g := p.CheckinGeneration("u1"); g != 2 {
		t.Errorf("generation=%d want 2 (upstream keeps generation, only backoff retires)", g)
	}
	if f.claimCalls.Load() != 0 {
		t.Errorf("claim calls=%d want 0 when already checked in", f.claimCalls.Load())
	}
}

func TestRunCheckinStatusRateLimitedRotates(t *testing.T) {
	var mu sync.Mutex
	statusCalls := 0
	claimCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/checkin_credits/status"):
			statusCalls++
			if statusCalls == 1 {
				w.Write([]byte(`{"code":9074,"message":"too many users"}`))
				return
			}
			w.Write([]byte(`{"checked_in":false,"credits":200,"enable":true}`))
		case strings.HasSuffix(r.URL.Path, "/checkin_credits/claim"):
			claimCalls++
			w.Write([]byte(`{"code":0,"message":"success"}`))
		case strings.HasSuffix(r.URL.Path, "/ide_user_ent_usage"):
			w.Write([]byte(`{"is_credits_billing":true,"user_entitlement_pack_list":[{"entitlement_base_info":{"quota":{"credits_limit":500}}}]}`))
		default:
			http.Error(w, "not found", 404)
		}
	}))
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	up := &upstream.Client{HTTP: srv.Client(), AgentHost: srv.URL, UgHost: srv.URL, OAuthHost: srv.URL, ClientID: upstream.ClientID}
	s := New(Config{Pool: p, Upstream: up, CheckinHour: 9, RefreshHours: []int{3}, RefreshSkew: time.Hour})
	s.RunCheckinNow()
	if g := p.CheckinGeneration("u1"); g != 1 {
		t.Errorf("generation=%d want 1 after status-9074 rotation", g)
	}
	if claimCalls != 1 {
		t.Errorf("claim calls=%d want 1 (retry with rotated id)", claimCalls)
	}
}

func TestRunCheckinRetriesDueAccount(t *testing.T) {
	claimCalls := atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/checkin_credits/status"):
			w.Write([]byte(`{"checked_in":false,"credits":200,"enable":true}`))
		case strings.HasSuffix(r.URL.Path, "/checkin_credits/claim"):
			claimCalls.Add(1)
			w.Write([]byte(`{"code":0,"message":"success"}`))
		case strings.HasSuffix(r.URL.Path, "/ide_user_ent_usage"):
			w.Write([]byte(`{"is_credits_billing":true,"user_entitlement_pack_list":[{"entitlement_base_info":{"quota":{"credits_limit":500}}}]}`))
		default:
			http.Error(w, "not found", 404)
		}
	}))
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	s := newTestScheduler(&fakeUpstream{resourceRemain: 500}, p, srv)
	s.RunCheckinRetries()
	if claimCalls.Load() != 0 {
		t.Fatalf("no due retry must not claim, calls=%d", claimCalls.Load())
	}
	p.NoteCheckinRateLimited("u1")
	// 人为将退避截止提前到过去，模拟到达重试时间（避免测试 sleep）。
	p.SetCheckinRetryAfterForTest("u1", time.Now().Add(-time.Second))
	s.RunCheckinRetries()
	if claimCalls.Load() != 1 {
		t.Fatalf("due retry must claim once, calls=%d", claimCalls.Load())
	}
	if p.CheckinRetryDue("u1") {
		t.Fatal("successful retry must clear backoff")
	}
}

func TestRunCheckinRefreshesRevokedTokenOnce(t *testing.T) {
	var mu sync.Mutex
	statusCalls, refreshCalls := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/checkin_credits/status"):
			statusCalls++
			if statusCalls == 1 {
				w.WriteHeader(401)
				w.Write([]byte(`{"code":1001,"message":"login required"}`))
				return
			}
			w.Write([]byte(`{"checked_in":true,"credits":200,"enable":true}`))
		case strings.HasSuffix(r.URL.Path, "/ExchangeToken"):
			refreshCalls++
			w.Write([]byte(`{"Result":{"Token":"newat","RefreshToken":"newrt","TokenExpireAt":1999999999,"TokenExpireDuration":1209600}}`))
		case strings.HasSuffix(r.URL.Path, "/ide_user_ent_usage"):
			w.Write([]byte(`{"is_credits_billing":true,"user_entitlement_pack_list":[{"entitlement_base_info":{"quota":{"credits_limit":500}}}]}`))
		default:
			http.Error(w, "not found", 404)
		}
	}))
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "old", RefreshToken: "rt", ExpiresAt: 9999999999, ApiHost: srv.URL})
	up := &upstream.Client{HTTP: srv.Client(), AgentHost: srv.URL, UgHost: srv.URL, OAuthHost: srv.URL, ClientID: upstream.ClientID}
	s := New(Config{Pool: p, Upstream: up, CheckinHour: 9, RefreshHours: []int{3}, RefreshSkew: time.Hour})
	s.RunCheckinNow()
	if refreshCalls != 1 {
		t.Errorf("refresh calls=%d want exactly 1", refreshCalls)
	}
	if statusCalls != 2 {
		t.Errorf("status calls=%d want 2 (fail + retry)", statusCalls)
	}
	if st, _ := p.Status("u1"); st.Disabled {
		t.Errorf("revoked-but-refreshable account must not be disabled: %+v", st)
	}
}

func TestRunCheckinPinnedDeviceNeverRotates(t *testing.T) {
	t.Setenv("TRAE_CHECKIN_DEVICE_ID", "pinned-device-1")
	f := &fakeUpstream{resourceRemain: 500}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/checkin_credits/status"):
			f.checkinCalls.Add(1)
			w.Write([]byte(`{"checked_in":false,"credits":200,"enable":true}`))
		case strings.HasSuffix(r.URL.Path, "/checkin_credits/claim"):
			f.claimCalls.Add(1)
			w.Write([]byte(`{"code":9074,"message":"too many users"}`))
		case strings.HasSuffix(r.URL.Path, "/ide_user_ent_usage"):
			w.Write([]byte(`{"is_credits_billing":true,"user_entitlement_pack_list":[{"entitlement_base_info":{"quota":{"credits_limit":500}}}]}`))
		default:
			http.Error(w, "not found", 404)
		}
	}))
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	s := newTestScheduler(f, p, srv)
	s.RunCheckinNow()
	if g := p.CheckinGeneration("u1"); g != 0 {
		t.Errorf("generation=%d want 0 (pinned device must not rotate)", g)
	}
}

func TestParseCheckinTimes(t *testing.T) {
	slots := parseCheckinTimes([]string{"07:45", " 17:15 ", "bad", "25:00"}, 9)
	if len(slots) != 2 {
		t.Fatalf("slots=%v want 2 (invalid entries skipped)", slots)
	}
	if slots[0].h != 7 || slots[0].m != 45 || slots[1].h != 17 || slots[1].m != 15 {
		t.Errorf("slots=%v want sorted 07:45,17:15", slots)
	}
	slots = parseCheckinTimes(nil, 9)
	if len(slots) != 1 || slots[0].h != 9 || slots[0].m != 0 {
		t.Errorf("fallback slots=%v want 09:00", slots)
	}
}

func TestNextCheckinFire(t *testing.T) {
	s := New(Config{CheckinTimes: []string{"07:45", "17:15"}, CheckinHour: 9, RefreshHours: []int{3}, RefreshSkew: time.Hour})

	// 清晨 → 今天的 07:45±10min
	now := time.Date(2026, 9, 30, 6, 0, 0, 0, time.Local)
	next := s.nextCheckinFire(now)
	if next.Day() != 30 || next.Hour() != 7 {
		t.Fatalf("next=%v want today 07:xx", next)
	}
	nominal := time.Date(2026, 9, 30, 7, 45, 0, 0, time.Local)
	if d := next.Sub(nominal); d < -checkinJitterMax || d > checkinJitterMax {
		t.Errorf("next=%v off nominal by %v, want within ±%v", next, d, checkinJitterMax)
	}
	if again := s.nextCheckinFire(now); !again.Equal(next) {
		t.Errorf("jitter not deterministic: %v vs %v", again, next)
	}

	// 上午 8 点（07:45 窗口已过）→ 今天 17:15±10min
	now = time.Date(2026, 9, 30, 8, 0, 0, 0, time.Local)
	next = s.nextCheckinFire(now)
	nominal = time.Date(2026, 9, 30, 17, 15, 0, 0, time.Local)
	if next.Day() != 30 || next.Hour() != 17 {
		t.Fatalf("next=%v want today 17:xx", next)
	}
	if d := next.Sub(nominal); d < -checkinJitterMax || d > checkinJitterMax {
		t.Errorf("next=%v off nominal by %v", next, d)
	}

	// 深夜 → 明天第一个时刻
	now = time.Date(2026, 9, 30, 23, 0, 0, 0, time.Local)
	next = s.nextCheckinFire(now)
	if next.Month() != time.October || next.Day() != 1 || next.Hour() != 7 {
		t.Fatalf("next=%v want Oct 1 07:xx", next)
	}
}

func TestRunCheckinSoftRateSchedulesRetry(t *testing.T) {
	f := &fakeUpstream{resourceRemain: 500}
	f.softRateClaim.Store(true)
	srv := f.server()
	defer srv.Close()

	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1", AccessToken: "at", RefreshToken: "rt", ExpiresAt: 9999999999})
	s := newTestScheduler(f, p, srv)

	s.RunCheckinNow()
	if f.claimCalls.Load() != 1 {
		t.Fatalf("claim calls=%d want 1 (no immediate retry on 429)", f.claimCalls.Load())
	}
	if p.CheckinRetryDue("u1") {
		t.Errorf("retry must not be due inside backoff window")
	}
	if p.CheckinGeneration("u1") != 0 {
		t.Errorf("generation=%d want 0 (429 must not rotate device)", p.CheckinGeneration("u1"))
	}

	// 退避到期（时间旅行）后重试成功
	f.softRateClaim.Store(false)
	p.SetCheckinRetryAfterForTest("u1", time.Now().Add(-time.Second))
	s.RunCheckinRetries()
	if f.claimCalls.Load() != 2 {
		t.Fatalf("claim calls=%d want 2 after backoff retry", f.claimCalls.Load())
	}
	if p.CheckinRetryDue("u1") {
		t.Errorf("retry state should be cleared after success")
	}
	if st, _ := p.Status("u1"); st.Credits != 500 {
		t.Errorf("credits=%d want 500 after successful retry", st.Credits)
	}
}
