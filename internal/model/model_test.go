package model

import (
	"sync"
	"testing"
)

func TestSetTablePrefix_TableNames(t *testing.T) {
	tests := []struct {
		prefix          string
		wantMessages    string
		wantSeqCursors  string
		wantMediaTasks  string
	}{
		{
			prefix:         "2026-04-11-03",
			wantMessages:   "corp_2026-04-11-03_messages",
			wantSeqCursors: "corp_2026-04-11-03_seq_cursors",
			wantMediaTasks: "corp_2026-04-11-03_media_tasks",
		},
		{
			prefix:         "2026-04-11-04",
			wantMessages:   "corp_2026-04-11-04_messages",
			wantSeqCursors: "corp_2026-04-11-04_seq_cursors",
			wantMediaTasks: "corp_2026-04-11-04_media_tasks",
		},
		{
			prefix:         "2026-04-12",
			wantMessages:   "corp_2026-04-12_messages",
			wantSeqCursors: "corp_2026-04-12_seq_cursors",
			wantMediaTasks: "corp_2026-04-12_media_tasks",
		},
	}

	for _, tt := range tests {
		SetTablePrefix(tt.prefix)

		if got := (Message{}).TableName(); got != tt.wantMessages {
			t.Errorf("prefix=%q: Message.TableName() = %q, want %q", tt.prefix, got, tt.wantMessages)
		}
		if got := (CorpSeqCursor{}).TableName(); got != tt.wantSeqCursors {
			t.Errorf("prefix=%q: CorpSeqCursor.TableName() = %q, want %q", tt.prefix, got, tt.wantSeqCursors)
		}
		if got := (MediaTask{}).TableName(); got != tt.wantMediaTasks {
			t.Errorf("prefix=%q: MediaTask.TableName() = %q, want %q", tt.prefix, got, tt.wantMediaTasks)
		}
	}
}

func TestGetDBPrefix(t *testing.T) {
	SetTablePrefix("2026-04-11-15")
	if got := GetDBPrefix(); got != "2026-04-11-15" {
		t.Errorf("GetDBPrefix() = %q, want %q", got, "2026-04-11-15")
	}

	SetTablePrefix("2026-04-11-16")
	if got := GetDBPrefix(); got != "2026-04-11-16" {
		t.Errorf("GetDBPrefix() = %q, want %q", got, "2026-04-11-16")
	}
}

func TestGetFilePrefix(t *testing.T) {
	SetFilePrefix("2026-04-11-15")
	if got := GetFilePrefix(); got != "2026-04-11-15" {
		t.Errorf("GetFilePrefix() = %q, want %q", got, "2026-04-11-15")
	}

	SetFilePrefix("2026-04-11-16")
	if got := GetFilePrefix(); got != "2026-04-11-16" {
		t.Errorf("GetFilePrefix() = %q, want %q", got, "2026-04-11-16")
	}
}

func TestTablePrefix_Concurrent(t *testing.T) {
	// 并发读写前缀，验证不会 panic 或 data race
	var wg sync.WaitGroup
	prefixes := []string{"2026-01", "2026-02", "2026-03", "2026-04"}

	// 并发写 DB 前缀
	for _, p := range prefixes {
		wg.Add(1)
		go func(prefix string) {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				SetTablePrefix(prefix)
			}
		}(p)
	}

	// 并发写 File 前缀
	for _, p := range prefixes {
		wg.Add(1)
		go func(prefix string) {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				SetFilePrefix(prefix + "-15")
			}
		}(p)
	}

	// 并发读
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 1000; i++ {
				_ = GetDBPrefix()
				_ = GetFilePrefix()
				_ = (Message{}).TableName()
				_ = (CorpSeqCursor{}).TableName()
				_ = (MediaTask{}).TableName()
			}
		}()
	}

	wg.Wait()

	// 验证最终状态一致：前缀和表名应该匹配
	prefix := GetDBPrefix()
	expected := "corp_" + prefix + "_messages"
	if got := (Message{}).TableName(); got != expected {
		t.Errorf("最终状态不一致: TableName()=%q, 期望前缀=%q", got, prefix)
	}
}

func TestTablePrefix_SwitchAndVerify(t *testing.T) {
	// 模拟小时切换：03 → 04，验证所有表名同步变化
	SetTablePrefix("2026-04-11-03")

	if got := (Message{}).TableName(); got != "corp_2026-04-11-03_messages" {
		t.Fatalf("切换前: %q", got)
	}

	// 模拟切换到 04
	SetTablePrefix("2026-04-11-04")

	msg := (Message{}).TableName()
	cursor := (CorpSeqCursor{}).TableName()
	media := (MediaTask{}).TableName()

	if msg != "corp_2026-04-11-04_messages" {
		t.Errorf("切换后 Message: %q", msg)
	}
	if cursor != "corp_2026-04-11-04_seq_cursors" {
		t.Errorf("切换后 CorpSeqCursor: %q", cursor)
	}
	if media != "corp_2026-04-11-04_media_tasks" {
		t.Errorf("切换后 MediaTask: %q", media)
	}
}
