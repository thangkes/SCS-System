package app

import (
	"context"
	"testing"
	"time"
)

// pulseStub: Registry trả Pulse do test đặt.
type pulseStub struct {
	*fakeRegistry
	p Pulse
}

func (s *pulseStub) Pulse(context.Context, time.Time) (Pulse, error) { return s.p, nil }

// Trạng thái đổi (cảnh báo mới mở, lên critical, đóng…) đẩy ngay; chỉ số đếm của cảnh báo đang mở tăng
// thì gom, đẩy tối đa mỗi liveCoalesce; nudgeLive đánh thức nơi phát đang chạy.
func TestLiveCoalesce(t *testing.T) {
	stub := &pulseStub{fakeRegistry: newFakeRegistry()}
	l := NewLive(Deps{Registry: stub})
	ch, _, ok := l.subscribe()
	if !ok {
		t.Fatal("không mở được luồng")
	}
	ctx := context.Background()
	got := func() bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}
	t0 := time.Now().Add(-time.Hour)
	stub.p = Pulse{OpenAlerts: 1, Serious: 1, LastOpen: t0, LastChange: t0}
	l.poll(ctx)
	if !got() {
		t.Fatal("gói đầu phải đẩy")
	}
	stub.p.LastChange = t0.Add(time.Minute) // cảnh báo đang mở chỉ cộng thêm số lần
	l.poll(ctx)
	if got() {
		t.Fatal("chỉ số đếm đổi: phải gom, không đẩy ngay")
	}
	stub.p.Critical = 1 // nâng lên critical: trạng thái đổi
	l.poll(ctx)
	if !got() {
		t.Fatal("nâng mức phải đẩy ngay")
	}
	stub.p.LastChange = t0.Add(2 * time.Minute)
	l.pushed = time.Now().Add(-2 * liveCoalesce) // đã quá nhịp gom
	l.poll(ctx)
	if !got() {
		t.Fatal("quá nhịp gom: số đếm đổi phải được đẩy")
	}
	stub.p.LastAck = time.Now() // cảnh báo đóng
	l.poll(ctx)
	if !got() {
		t.Fatal("đóng cảnh báo phải đẩy ngay")
	}

	// nudgeLive đánh thức nơi phát hiện tại.
	select {
	case <-l.wake:
	default:
	}
	nudgeLive()
	select {
	case <-l.wake:
	default:
		t.Fatal("nudgeLive phải đánh thức luồng")
	}
}

// Dữ liệu trang Thiết bị LAN / Sức khỏe máy đổi (cảnh báo không đổi): đẩy, nhưng gom tối đa mỗi liveTopicEvery.
func TestLiveTopic(t *testing.T) {
	stub := &pulseStub{fakeRegistry: newFakeRegistry()}
	l := NewLive(Deps{Registry: stub})
	ch, _, _ := l.subscribe()
	ctx := context.Background()
	got := func() bool {
		select {
		case <-ch:
			return true
		default:
			return false
		}
	}
	t0 := time.Now().Add(-time.Hour)
	stub.p = Pulse{LastChange: t0}
	l.poll(ctx)
	if !got() {
		t.Fatal("gói đầu phải đẩy")
	}
	stub.p.LANAt = time.Now()
	l.poll(ctx)
	if got() {
		t.Fatal("vừa đẩy xong: dữ liệu LAN đổi phải chờ liveTopicEvery")
	}
	l.pushed = time.Now().Add(-2 * liveTopicEvery)
	l.poll(ctx)
	if !got() {
		t.Fatal("quá liveTopicEvery: dữ liệu LAN đổi phải được đẩy")
	}
	if lanDataAt.Load() != stub.p.LANAt.UnixNano() {
		t.Fatal("lanDataAt phải theo lần dữ liệu LAN đổi (bỏ bộ đệm trang LAN)")
	}
	l.poll(ctx)
	if got() {
		t.Fatal("không đổi gì mà vẫn đẩy")
	}
	stub.p.HealthAt = time.Now()
	l.pushed = time.Now().Add(-2 * liveTopicEvery)
	l.poll(ctx)
	if !got() {
		t.Fatal("sức khỏe máy đổi phải được đẩy")
	}
}
