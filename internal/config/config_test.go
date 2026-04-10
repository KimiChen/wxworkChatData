package config

import (
	"testing"
	"time"
)

func TestFormatStoragePrefix(t *testing.T) {
	now := time.Now()

	tests := []struct {
		format   string
		expected string
	}{
		{"YYYY", now.Format("2006")},
		{"YYYY-MM", now.Format("2006-01")},
		{"YYYY-MM-DD", now.Format("2006-01-02")},
		{"YYYY-MM-DD-hh", now.Format("2006-01-02-15")},
		{"YYYY-MM-DD-hh-mm", now.Format("2006-01-02-15-04")},
		{"YYYY-MM-DD-hh-mm-ss", now.Format("2006-01-02-15-04-05")},
	}

	for _, tt := range tests {
		result := FormatStoragePrefix(tt.format)
		if result != tt.expected {
			t.Errorf("FormatStoragePrefix(%q) = %q, want %q", tt.format, result, tt.expected)
		}
	}
}

func TestFormatStoragePrefix_ChangesWithTime(t *testing.T) {
	// 验证不同秒生成的 ss 级前缀会变化
	format := "YYYY-MM-DD-hh-mm-ss"
	p1 := FormatStoragePrefix(format)
	time.Sleep(1100 * time.Millisecond)
	p2 := FormatStoragePrefix(format)

	if p1 == p2 {
		t.Errorf("秒级前缀在 1 秒后应该变化: p1=%q p2=%q", p1, p2)
	}

	// 验证分钟级前缀在同一分钟内不变
	format2 := "YYYY-MM-DD-hh-mm"
	p3 := FormatStoragePrefix(format2)
	p4 := FormatStoragePrefix(format2)
	if p3 != p4 {
		t.Errorf("分钟级前缀在同一瞬间应该相同: p3=%q p4=%q", p3, p4)
	}
}
