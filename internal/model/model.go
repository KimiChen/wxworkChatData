package model

import (
	"encoding/json"
	"sync"
	"time"

	"gorm.io/gorm"
)

var (
	dbPrefix    string
	filePrefix  string
	tablePrefix = "corp_"
	prefixMu    sync.RWMutex
)

// SetTablePrefix 设置数据库表名前缀（并发安全）
func SetTablePrefix(prefix string) {
	prefixMu.Lock()
	defer prefixMu.Unlock()
	dbPrefix = prefix
	if prefix != "" {
		tablePrefix = "corp_" + prefix + "_"
	}
}

// GetDBPrefix 获取当前数据库表名前缀（并发安全）
func GetDBPrefix() string {
	prefixMu.RLock()
	defer prefixMu.RUnlock()
	return dbPrefix
}

// SetFilePrefix 设置媒体文件目录前缀（并发安全）
func SetFilePrefix(prefix string) {
	prefixMu.Lock()
	defer prefixMu.Unlock()
	filePrefix = prefix
}

// GetFilePrefix 获取当前媒体文件目录前缀（并发安全）
func GetFilePrefix() string {
	prefixMu.RLock()
	defer prefixMu.RUnlock()
	return filePrefix
}

func getTablePrefix() string {
	prefixMu.RLock()
	defer prefixMu.RUnlock()
	return tablePrefix
}

// Message 会话存档消息
type Message struct {
	ID           uint64          `gorm:"primaryKey;autoIncrement"`
	CorpName     string          `gorm:"type:varchar(64);not null;uniqueIndex:uk_corp_msgid"`
	MsgID        string          `gorm:"type:varchar(128);not null;uniqueIndex:uk_corp_msgid"`
	Seq          uint64          `gorm:"not null;index:idx_corp_seq,priority:2"`
	Action       string          `gorm:"type:varchar(16);not null;default:send"`
	FromUser     string          `gorm:"column:from_user;type:varchar(128);not null;default:''"`
	ToList       json.RawMessage `gorm:"type:json"`
	RoomID       string          `gorm:"type:varchar(128);not null;default:''"`
	MsgType      string          `gorm:"type:varchar(32);not null;default:''"`
	MsgTime      int64           `gorm:"not null;default:0;index:idx_msg_time"`
	Content      json.RawMessage `gorm:"type:json"`
	PublickeyVer uint32          `gorm:"not null;default:0"`
	CreatedAt    time.Time       `gorm:"autoCreateTime:milli"`
}

func (Message) TableName() string { return getTablePrefix() + "messages" }

// CorpSeqCursor 每个企业的消息拉取游标
type CorpSeqCursor struct {
	ID        uint64    `gorm:"primaryKey;autoIncrement"`
	CorpName  string    `gorm:"type:varchar(64);not null;uniqueIndex:uk_corp"`
	LastSeq   uint64    `gorm:"not null;default:0"`
	UpdatedAt time.Time `gorm:"autoUpdateTime:milli"`
}

func (CorpSeqCursor) TableName() string { return getTablePrefix() + "seq_cursors" }

// MediaTask 媒体文件下载任务
// Status: 0=pending, 1=downloading, 2=done, 3=failed
type MediaTask struct {
	ID         uint64    `gorm:"primaryKey;autoIncrement"`
	CorpName   string    `gorm:"type:varchar(64);not null;uniqueIndex:uk_corp_msg_type"`
	MsgID      string    `gorm:"type:varchar(128);not null;uniqueIndex:uk_corp_msg_type"`
	MsgType    string    `gorm:"type:varchar(32);not null;uniqueIndex:uk_corp_msg_type"`
	SdkFileID  string    `gorm:"type:text;not null"`
	Md5sum     string    `gorm:"type:varchar(64);not null;default:''"`
	FileSize   uint64    `gorm:"not null;default:0"`
	FileExt    string    `gorm:"type:varchar(16);not null;default:''"`
	FilePath   string    `gorm:"type:varchar(512);not null;default:''"`
	Status     int8      `gorm:"not null;default:0;index:idx_status"`
	RetryCount int       `gorm:"not null;default:0"`
	ErrorMsg   string    `gorm:"type:varchar(512);not null;default:''"`
	MsgTime    int64     `gorm:"not null;default:0"`
	CreatedAt  time.Time `gorm:"autoCreateTime:milli"`
	UpdatedAt  time.Time `gorm:"autoUpdateTime:milli"`
}

func (MediaTask) TableName() string { return getTablePrefix() + "media_tasks" }

// AutoMigrate 自动建表
func AutoMigrate(db *gorm.DB) error {
	return db.AutoMigrate(&Message{}, &CorpSeqCursor{}, &MediaTask{})
}
