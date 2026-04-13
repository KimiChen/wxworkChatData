package media

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"time"

	WeWorkFinanceSDK "github.com/NICEXAI/WeWorkFinanceSDK"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"wxworkChatData/internal/config"
	"wxworkChatData/internal/model"
)

const (
	StatusPending     int8 = 0
	StatusDownloading int8 = 1
	StatusDone        int8 = 2
	StatusFailed      int8 = 3

	maxRetries       = 3
	staleDuration    = 5 * time.Minute
	pollInterval     = 5 * time.Second
	batchSize        = 50
)

type Downloader struct {
	basePath     string
	corpName     string
	client       WeWorkFinanceSDK.Client
	proxy        string
	proxyPwd     string
	timeout      int
	db           *gorm.DB
	logger       *zap.Logger
	lastRecovery time.Time
}

func NewDownloader(basePath, corpName string, client WeWorkFinanceSDK.Client,
	proxyCfg config.ProxyConfig, timeout int, db *gorm.DB, logger *zap.Logger) *Downloader {
	return &Downloader{
		basePath: basePath,
		corpName: corpName,
		client:   client,
		proxy:    proxyCfg.URL,
		proxyPwd: proxyCfg.Password,
		timeout:  timeout,
		db:       db,
		logger:   logger.With(zap.String("component", "downloader"), zap.String("corp", corpName)),
	}
}

// Run starts the media download loop. Blocks until ctx is cancelled.
func (d *Downloader) Run(ctx context.Context) {
	d.logger.Info("media downloader started")
	defer d.logger.Info("media downloader stopped")

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// Recover stale downloading tasks (every 5 min, not every loop)
		if time.Since(d.lastRecovery) > staleDuration {
			d.recoverStaleTasks()
			d.lastRecovery = time.Now()
		}

		count, err := d.processPendingTasks(ctx)
		if err != nil {
			d.logger.Error("process pending tasks failed", zap.Error(err))
		}

		if count == 0 {
			select {
			case <-time.After(pollInterval):
			case <-ctx.Done():
				return
			}
		}
	}
}

func (d *Downloader) recoverStaleTasks() {
	result := d.db.Model(&model.MediaTask{}).
		Where("corp_name = ? AND status = ? AND updated_at < ?",
			d.corpName, StatusDownloading, time.Now().Add(-staleDuration)).
		Updates(map[string]interface{}{"status": StatusPending})
	if result.RowsAffected > 0 {
		d.logger.Info("recovered stale download tasks", zap.Int64("count", result.RowsAffected))
	}
}

func (d *Downloader) processPendingTasks(ctx context.Context) (int, error) {
	var tasks []model.MediaTask
	err := d.db.Where("corp_name = ? AND status = ? AND retry_count < ?",
		d.corpName, StatusPending, maxRetries).
		Order("created_at ASC").
		Limit(batchSize).
		Find(&tasks).Error
	if err != nil {
		return 0, fmt.Errorf("query pending tasks: %w", err)
	}

	for i := range tasks {
		select {
		case <-ctx.Done():
			return i, nil
		default:
		}

		if err := d.downloadOne(&tasks[i]); err != nil {
			d.logger.Error("download media failed",
				zap.String("msg_id", tasks[i].MsgID),
				zap.String("msg_type", tasks[i].MsgType),
				zap.Error(err))
		}
	}

	return len(tasks), nil
}

func (d *Downloader) downloadOne(task *model.MediaTask) error {
	// Mark as downloading
	d.db.Model(task).Updates(map[string]interface{}{
		"status": StatusDownloading,
	})

	data, err := d.fetchMediaData(task.SdkFileID)
	if err != nil {
		d.markFailed(task, err)
		return err
	}

	// MD5 verification
	if task.Md5sum != "" {
		hash := md5.Sum(data)
		actual := hex.EncodeToString(hash[:])
		if actual != task.Md5sum {
			err := fmt.Errorf("md5 mismatch: expected %s, got %s", task.Md5sum, actual)
			d.markFailed(task, err)
			return err
		}
	}

	// Build file path and write
	filePath := d.buildFilePath(task)
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		d.markFailed(task, err)
		return fmt.Errorf("create dir %s: %w", dir, err)
	}

	if err := os.WriteFile(filePath, data, 0644); err != nil {
		d.markFailed(task, err)
		return fmt.Errorf("write file %s: %w", filePath, err)
	}

	// Mark done
	d.db.Model(task).Updates(map[string]interface{}{
		"status":    StatusDone,
		"file_path": filePath,
		"error_msg": "",
	})

	d.logger.Debug("media downloaded",
		zap.String("msg_id", task.MsgID),
		zap.String("path", filePath),
		zap.Int("size", len(data)))

	return nil
}

func (d *Downloader) fetchMediaData(sdkFileID string) ([]byte, error) {
	var buf bytes.Buffer
	indexBuf := ""

	for {
		mediaData, err := d.client.GetMediaData(indexBuf, sdkFileID, d.proxy, d.proxyPwd, d.timeout)
		if err != nil {
			return nil, fmt.Errorf("GetMediaData: %w", err)
		}
		buf.Write(mediaData.Data)
		if mediaData.IsFinish {
			break
		}
		indexBuf = mediaData.OutIndexBuf
	}

	return buf.Bytes(), nil
}

func (d *Downloader) buildFilePath(task *model.MediaTask) string {
	ext := task.FileExt
	if ext == "" {
		ext = "bin"
	}

	// Sanitize msgid for filename (replace / with _)
	safeID := sanitizeFilename(task.MsgID)

	// 使用文件目录前缀作为时间目录（动态跟随 storage_file 配置）
	return filepath.Join(d.basePath, d.corpName, model.GetFilePrefix(), safeID+"."+ext)
}

func (d *Downloader) markFailed(task *model.MediaTask, err error) {
	errMsg := err.Error()
	if len(errMsg) > 500 {
		errMsg = errMsg[:500]
	}

	newRetry := task.RetryCount + 1
	status := StatusPending
	if newRetry >= maxRetries {
		status = StatusFailed
	}

	d.db.Model(task).Updates(map[string]interface{}{
		"status":      status,
		"retry_count": newRetry,
		"error_msg":   errMsg,
	})
}

func sanitizeFilename(s string) string {
	result := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '/' || c == '\\' || c == ':' || c == '*' || c == '?' || c == '"' || c == '<' || c == '>' || c == '|' {
			result = append(result, '_')
		} else {
			result = append(result, c)
		}
	}
	return string(result)
}
