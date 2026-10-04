// Package worker 定时任务：支付超时释放、灯控预热、自动开关灯、爽约判定。
package worker

import (
	"context"
	"log"
	"time"

	"courtbook/internal/domain"
	"courtbook/internal/service"
)

type Worker struct {
	svc       *service.Service
	tick      time.Duration
	stopCh    chan struct{}
	stopped   chan struct{}
	autostart bool
}

func New(svc *service.Service) *Worker {
	return &Worker{svc: svc, tick: 10 * time.Second, stopCh: make(chan struct{}), stopped: make(chan struct{}), autostart: true}
}

// WithTick 设置轮询间隔（测试用）
func (w *Worker) WithTick(d time.Duration) *Worker { w.tick = d; return w }

// WithAutoStart 设置是否后台自动轮询（false 时仅由 /api/admin/worker/run-once 手动触发）
func (w *Worker) WithAutoStart(b bool) *Worker { w.autostart = b; return w }

func (w *Worker) Start() {
	if !w.autostart {
		return
	}
	go func() {
		defer close(w.stopped)
		t := time.NewTicker(w.tick)
		defer t.Stop()
		w.RunOnce(context.Background())
		for {
			select {
			case <-w.stopCh:
				return
			case <-t.C:
				w.RunOnce(context.Background())
			}
		}
	}()
}

func (w *Worker) Stop() {
	if !w.autostart {
		close(w.stopped)
		return
	}
	close(w.stopCh)
	<-w.stopped
}

// RunOnce 执行一轮全部定时动作。顺序很重要：爽约判定先于自动关灯——
// 短场次可能在结束时刻才到爽约截止点，必须先给出爽约结论。
func (w *Worker) RunOnce(ctx context.Context) {
	if n, err := w.svc.ExpireStale(ctx); err != nil {
		log.Printf("worker expire: %v", err)
	} else if n > 0 {
		log.Printf("worker: 释放超时未支付订单 %d 笔", n)
	}
	if n, err := w.sweepWarmup(ctx); err != nil {
		log.Printf("worker warmup: %v", err)
	} else if n > 0 {
		log.Printf("worker: %d 笔订单进入灯控预热", n)
	}
	if n, err := w.sweepLightOn(ctx); err != nil {
		log.Printf("worker lighton: %v", err)
	} else if n > 0 {
		log.Printf("worker: %d 块场地自动开灯", n)
	}
	if rs, err := w.svc.SweepNoShow(ctx); err != nil {
		log.Printf("worker noshow: %v", err)
	} else if len(rs) > 0 {
		for _, r := range rs {
			log.Printf("worker: 订单 %d 爽约判定=%s 罚没=%d", r.BookingID, r.Decision, r.ForfeitCents)
		}
	}
	if n, err := w.sweepLightOff(ctx); err != nil {
		log.Printf("worker lightoff: %v", err)
	} else if n > 0 {
		log.Printf("worker: %d 块场地自动关灯并结算", n)
	}
}

// sweepWarmup paid_waiting 且到达 start-warmup_minutes → warmup
func (w *Worker) sweepWarmup(ctx context.Context) (int, error) {
	now := w.svc.Now()
	rows, err := w.svc.Store.Pool.Query(ctx, `
		SELECT b.id FROM bookings b JOIN venue_policies p ON p.venue_id=b.venue_id
		WHERE b.status='paid_waiting'
		  AND b.start_time - make_interval(mins => p.warmup_minutes) <= $1
		  AND b.start_time > $1
		ORDER BY b.id FOR UPDATE SKIP LOCKED`, now)
	if err != nil {
		return 0, err
	}
	ids := collectIDs(rows)
	n := 0
	for _, id := range ids {
		if _, err := w.svc.LightWarmup(ctx, id, ""); err == nil {
			n++
		}
	}
	return n, nil
}

// sweepLightOn warmup/paid_waiting 且到达 start → in_play（自动开灯）
func (w *Worker) sweepLightOn(ctx context.Context) (int, error) {
	now := w.svc.Now()
	rows, err := w.svc.Store.Pool.Query(ctx, `
		SELECT id FROM bookings
		WHERE status IN ('warmup','paid_waiting') AND start_time <= $1 AND end_time > $1
		ORDER BY id FOR UPDATE SKIP LOCKED`, now)
	if err != nil {
		return 0, err
	}
	ids := collectIDs(rows)
	n := 0
	for _, id := range ids {
		if _, err := w.svc.LightOn(ctx, id, ""); err == nil {
			n++
		}
	}
	return n, nil
}

// sweepLightOff in_play 且 end+comp 已过 → 关灯结算
func (w *Worker) sweepLightOff(ctx context.Context) (int, error) {
	now := w.svc.Now()
	rows, err := w.svc.Store.Pool.Query(ctx, `
		SELECT id FROM bookings
		WHERE status IN ('in_play','warmup') AND end_time + make_interval(mins => comp_minutes_granted) <= $1
		ORDER BY id FOR UPDATE SKIP LOCKED`, now)
	if err != nil {
		return 0, err
	}
	ids := collectIDs(rows)
	n := 0
	for _, id := range ids {
		if _, err := w.svc.LightOff(ctx, id, time.Time{}, "auto"); err == nil {
			n++
		}
	}
	return n, nil
}

// EnsureTerminal 兜底：paid_waiting 且 end_time 已过很久但没有灯信号（极端情况留给爽约扫描）
func (w *Worker) EnsureTerminal(_ context.Context) {}

var _ = domain.StatusWarmup

func collectIDs(rows interface {
	Next() bool
	Scan(...any) error
	Close()
	Err() error
}) []int64 {
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	return ids
}
