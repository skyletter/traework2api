// Package scheduler 定时任务：每日签到 + token 预刷新。
// 签到成功后重新查积分，积分 > 0 的冷却账号自动解冻。
package scheduler

import (
	"context"
	"errors"
	"log"
	"strings"
	"sync"
	"time"

	"trae2api-web/internal/auth"
	"trae2api-web/internal/pool"
	"trae2api-web/internal/upstream"
)

// Config 调度器依赖。
type Config struct {
	Pool         *pool.Pool
	Upstream     *upstream.Client
	CheckinHour  int           // 每日签到小时，默认 9
	RefreshHours []int         // token 预刷新小时，默认 [3]
	RefreshSkew  time.Duration // 预刷新窗口，默认 24h
}

// Scheduler 调度器。
type Scheduler struct {
	cfg Config

	checkinMu sync.Mutex // 串行化签到批次（每日/重试/启动补签），防同账号并发
}

// New 构建。
func New(cfg Config) *Scheduler {
	if cfg.CheckinHour < 0 {
		cfg.CheckinHour = 9
	}
	if len(cfg.RefreshHours) == 0 {
		cfg.RefreshHours = []int{3}
	}
	if cfg.RefreshSkew <= 0 {
		cfg.RefreshSkew = 24 * time.Hour
	}
	return &Scheduler{cfg: cfg}
}

// nextFire 返回 now 之后最近的一个整点触发时间；hours 为本地小时（0-23）。
func nextFire(now time.Time, hours []int) time.Time {
	var earliest time.Time
	for _, h := range hours {
		t := time.Date(now.Year(), now.Month(), now.Day(), h, 0, 0, 0, now.Location())
		if !t.After(now) {
			t = t.Add(24 * time.Hour)
		}
		if earliest.IsZero() || t.Before(earliest) {
			earliest = t
		}
	}
	return earliest
}

// Run 主循环，阻塞直到 ctx 取消。
func (s *Scheduler) Run(ctx context.Context) {
	all := append(append([]int{}, s.cfg.RefreshHours...), s.cfg.CheckinHour)
	for {
		next := nextFire(time.Now(), all)
		timer := time.NewTimer(time.Until(next))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			h := time.Now().Hour()
			if contains(s.cfg.RefreshHours, h) {
				s.RunRefreshNow()
			}
			if s.cfg.CheckinHour == h {
				s.RunCheckinNow()
			}
		}
	}
}

func contains(hours []int, h int) bool {
	for _, v := range hours {
		if v == h {
			return true
		}
	}
	return false
}

// RunCheckinNow 立即对所有账号执行签到 + 积分刷新 + 解冻。
// 冷却中的账号也参与（签到就是为了解冻它们）；禁用的跳过。
func (s *Scheduler) RunCheckinNow() {
	s.checkinMu.Lock()
	defer s.checkinMu.Unlock()
	for _, st := range s.cfg.Pool.List() {
		if st.Disabled {
			continue
		}
		a := s.cfg.Pool.AuthByUID(st.UID)
		if a == nil || a.RefreshTokenValue() == "" {
			continue
		}
		_ = s.checkinAccount(st, a)
		s.refreshCredits(st, a)
	}
}

// RunCheckinRetries 对退避到期的账号补一次签到（intraday 重试）。
// 退避公式与持久化见 pool.NoteCheckinRateLimited；成功/已签到清零。
// Source: autumnsentiment/Trae2api-cn @ 2403954 + @ 165ac6e
func (s *Scheduler) RunCheckinRetries() {
	s.checkinMu.Lock()
	defer s.checkinMu.Unlock()
	for _, st := range s.cfg.Pool.List() {
		if st.Disabled {
			continue
		}
		if !s.cfg.Pool.CheckinRetryDue(st.UID) {
			continue
		}
		a := s.cfg.Pool.AuthByUID(st.UID)
		if a == nil || a.RefreshTokenValue() == "" {
			continue
		}
		_ = s.checkinAccount(st, a)
		s.refreshCredits(st, a)
	}
}

// RunStartupCheckin 启动补检查：对全部账号「查签到 → 未签则签」并刷新积分，
// 返回每账号结果（供启动运行报告展示）。语义与每日定时任务一致：
// 已签到的账号快速跳过；冷却账号也参与（签到可解冻）；硬禁用跳过。
func (s *Scheduler) RunStartupCheckin() []CheckinOutcome {
	s.checkinMu.Lock()
	defer s.checkinMu.Unlock()
	out := make([]CheckinOutcome, 0)
	for _, st := range s.cfg.Pool.List() {
		if st.Disabled {
			out = append(out, CheckinOutcome{UID: st.UID, Nickname: st.Nickname, Result: "skipped", Detail: "已禁用"})
			continue
		}
		a := s.cfg.Pool.AuthByUID(st.UID)
		if a == nil || a.RefreshTokenValue() == "" {
			out = append(out, CheckinOutcome{UID: st.UID, Nickname: st.Nickname, Result: "skipped", Detail: "凭证缺失"})
			continue
		}
		out = append(out, s.checkinAccount(st, a))
		s.refreshCredits(st, a)
	}
	return out
}

// RetryLoop 每 interval 扫一次到期重试；ctx 取消时返回。
func (s *Scheduler) RetryLoop(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.RunCheckinRetries()
		}
	}
}

// CheckinOutcome 单账号签到结果（供启动运行报告等消费）。
type CheckinOutcome struct {
	UID      string
	Nickname string
	Result   string // ok | already | not_enabled | rate_limited | error | skipped
	Detail   string // 附注（退避时刻/错误摘要），可空
}

// who 返回账号展示名（昵称优先，附 uid 便于检索）。
func who(st pool.Status) string {
	if st.Nickname != "" {
		return st.Nickname + " (" + st.UID + ")"
	}
	return st.UID
}

// checkinAccount 单账号一次 status→claim。9074 时轮换设备并记录退避
// （status 限流换 ID 重查一次，claim 限流等退避到期）；成功/已签到清零退避。
// 返回结果供启动运行报告汇总。
func (s *Scheduler) checkinAccount(st pool.Status, a *auth.Auth) CheckinOutcome {
	out := CheckinOutcome{UID: st.UID, Nickname: st.Nickname}
	identity := upstream.CheckinIdentity(a)
	deviceID, pinned := upstream.CheckinDevice(identity, s.cfg.Pool.CheckinGeneration(st.UID))
	checkedIn, _, enable, err := s.statusWithRefresh(a, deviceID)
	if errors.Is(err, upstream.ErrCheckinRateLimited) && !pinned {
		gen := s.cfg.Pool.BumpCheckinGeneration(st.UID)
		deviceID, _ = upstream.CheckinDevice(identity, gen)
		log.Printf("checkin status %s: rate limited (9074), rotated device to gen %d", st.UID, gen)
		checkedIn, _, enable, err = s.statusWithRefresh(a, deviceID)
	}
	if err != nil {
		log.Printf("checkin status %s: %v", who(st), err)
		out.Result, out.Detail = "error", err.Error()
		return out
	}
	if checkedIn {
		s.cfg.Pool.ClearCheckinRetry(st.UID)
		log.Printf("checkin %s: already checked in", who(st))
		out.Result = "already"
		return out
	}
	if !enable {
		out.Result = "not_enabled"
		return out
	}
	if err := s.cfg.Upstream.CheckinClaim(a, deviceID); err != nil {
		if errors.Is(err, upstream.ErrCheckinRateLimited) {
			if !pinned {
				newGen := s.cfg.Pool.BumpCheckinGeneration(st.UID)
				log.Printf("checkin claim %s: rate limited (9074), rotated device to gen %d", st.UID, newGen)
			}
			after := s.cfg.Pool.NoteCheckinRateLimited(st.UID)
			log.Printf("checkin claim %s: retry after %s", who(st), after.Format("15:04:05"))
			out.Result = "rate_limited"
			out.Detail = "重试 " + after.Format("15:04:05")
		} else {
			log.Printf("checkin claim %s: %v", who(st), err)
			out.Result, out.Detail = "error", err.Error()
		}
		return out
	}
	s.cfg.Pool.ClearCheckinRetry(st.UID)
	log.Printf("checkin %s: ok", who(st))
	out.Result = "ok"
	return out
}

// statusWithRefresh 查签到状态；401/1001 认证失败时刷新 token 落盘后只重试一次。
// Source: autumnsentiment/Trae2api-cn @ 87a510a（失败刷新重试一次）。
// 用 RefreshToken（无条件换新；吊销的 token 不会触发 NeedsRefresh），
// 持锁串行，调用方不得并发对同一账号调此函数。
func (s *Scheduler) statusWithRefresh(a *auth.Auth, deviceID string) (bool, int64, bool, error) {
	checkedIn, credits, enable, err := s.cfg.Upstream.CheckinStatus(a, deviceID)
	if err == nil || !isCheckinAuthFailure(err) {
		return checkedIn, credits, enable, err
	}
	uid := a.UID
	if rerr := s.cfg.Upstream.RefreshToken(a); rerr != nil {
		return false, 0, false, err
	}
	if serr := a.SaveAtomic(); serr != nil {
		log.Printf("checkin %s refresh save: %v", uid, serr)
	}
	return s.cfg.Upstream.CheckinStatus(a, deviceID)
}

// isCheckinAuthFailure 报告签到错误是否为认证失效（HTTP 401 会话失效，
// 或业务码 1001）。其他业务码（9074/9004/9095 等）不是。
func isCheckinAuthFailure(err error) bool {
	var ue *upstream.Error
	if errors.As(err, &ue) && ue.Kind == upstream.ErrSessionDead {
		return true
	}
	return strings.Contains(err.Error(), "1001")
}

// refreshCredits 查积分 + 解冻（有积分的冷却账号恢复）。
func (s *Scheduler) refreshCredits(st pool.Status, a *auth.Auth) {
	remain, err := s.cfg.Upstream.UserEntUsage(a)
	if err != nil {
		log.Printf("ent-usage %s: %v", who(st), err)
		return
	}
	s.cfg.Pool.ReenableIfCredits(st.UID, remain)
	log.Printf("credits %s: remain=%d", who(st), remain)
}

// RunRefreshNow 立即对所有账号刷新 token；session 失效的自动禁用。
func (s *Scheduler) RunRefreshNow() {
	for _, st := range s.cfg.Pool.List() {
		if st.Disabled {
			continue
		}
		a := s.cfg.Pool.AuthByUID(st.UID)
		if a == nil || a.RefreshTokenValue() == "" {
			continue
		}
		// 持锁内重查，避免与签到路径的刷新并发重复 ExchangeToken。
		// 注意：失败时返回 (false, err)，必须先判 err 再判 refreshed。
		refreshed, err := s.cfg.Upstream.RefreshTokenIfNeeded(a, s.cfg.RefreshSkew)
		if err != nil {
			log.Printf("refresh %s: %v", st.UID, err)
			var ue *upstream.Error
			if errors.As(err, &ue) && ue.Kind == upstream.ErrSessionDead {
				s.cfg.Pool.Disable(st.UID, "session dead")
			}
			continue
		}
		if !refreshed {
			continue
		}
		if err := a.SaveAtomic(); err != nil {
			log.Printf("refresh %s save: %v", st.UID, err)
		}
	}
}
