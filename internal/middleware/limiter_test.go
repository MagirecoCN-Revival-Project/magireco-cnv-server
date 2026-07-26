package middleware

import (
	"testing"
	"time"
)

// TestLimiter_Allow 锁定 Allow(key) 的语义:窗口内前 N 次放行,之后拒;
// 窗口过期后重置;不同 key 互不干扰。
func TestLimiter_Allow(t *testing.T) {
	l := NewLimiter("test", 2, 50*time.Millisecond, "msg", nil)

	// 同 key 前 2 次放行,第 3 次拒
	for i := 0; i < 2; i++ {
		if !l.Allow("k1") {
			t.Errorf("第 %d 次应当放行", i+1)
		}
	}
	if l.Allow("k1") {
		t.Errorf("第 3 次应当拒绝")
	}
	if l.Allow("k1") {
		t.Errorf("第 4 次仍然应当拒绝(在窗口内)")
	}

	// 另一个 key 不受影响
	if !l.Allow("k2") {
		t.Errorf("不同 key 应当独立计数")
	}

	// 等到窗口过期,k1 应当重新放行
	time.Sleep(60 * time.Millisecond)
	if !l.Allow("k1") {
		t.Errorf("窗口过期后 k1 应当重新放行")
	}
}
