package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	gormlogger "gorm.io/gorm/logger"

	"wxworkChatData/internal/config"
	"wxworkChatData/internal/model"
	"wxworkChatData/internal/worker"
)

func main() {
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	flag.Parse()

	// 1. Load config
	cfg, err := config.LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}

	// 2. Init logger
	logger, err := initLogger(cfg.Log)
	if err != nil {
		fmt.Fprintf(os.Stderr, "初始化日志失败: %v\n", err)
		os.Exit(1)
	}
	defer logger.Sync()

	// 3. Set initial table prefix
	model.SetTablePrefix(cfg.StoragePrefix)

	logger.Info("企业微信会话存档服务启动中",
		zap.Int("corp_count", len(cfg.Corps)),
		zap.String("storage_filewords", cfg.StorageFilewords),
		zap.String("storage_prefix", cfg.StoragePrefix),
		zap.String("proxy", cfg.Proxy.URL))

	// 4. Init MySQL
	gormCfg := &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	}
	db, err := gorm.Open(mysql.Open(cfg.MySQL.DSN), gormCfg)
	if err != nil {
		logger.Fatal("连接数据库失败", zap.Error(err))
	}

	sqlDB, err := db.DB()
	if err != nil {
		logger.Fatal("获取数据库连接池失败", zap.Error(err))
	}
	sqlDB.SetMaxOpenConns(cfg.MySQL.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MySQL.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(time.Hour)

	if err := sqlDB.Ping(); err != nil {
		logger.Fatal("数据库连接验证失败", zap.Error(err))
	}

	// 5. Auto-migrate tables
	if err := model.AutoMigrate(db); err != nil {
		logger.Fatal("数据库建表失败", zap.Error(err))
	}
	logger.Info("数据库表结构就绪")

	// 6. 启动时迁移：如果当前表没有游标，从最近的旧表迁移
	migrateFromPreviousTable(cfg.StoragePrefix, db, logger)

	// 6. Validate media path
	if cfg.Media.BasePath != "" {
		if err := os.MkdirAll(cfg.Media.BasePath, 0755); err != nil {
			logger.Fatal("媒体目录创建/访问失败", zap.String("path", cfg.Media.BasePath), zap.Error(err))
		}
		logger.Info("媒体存储目录就绪", zap.String("path", cfg.Media.BasePath))
	}

	// 7. Setup graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	var wg sync.WaitGroup

	// 8. Start table prefix rotation goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		runPrefixRotation(ctx, cfg.StorageFilewords, db, logger)
	}()

	// 9. Start workers for each corp
	for _, corpCfg := range cfg.Corps {
		w, err := worker.NewWorker(corpCfg, cfg.Proxy, cfg.Media, db, logger)
		if err != nil {
			logger.Error("创建 worker 失败", zap.String("corp", corpCfg.Name), zap.Error(err))
			continue
		}

		wg.Add(1)
		corpName := corpCfg.Name
		go func() {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					logger.Error("worker panic recovered",
						zap.String("corp", corpName),
						zap.Any("panic", r))
				}
			}()
			w.Run(ctx)
		}()

		logger.Info("worker 已启动", zap.String("corp", corpName))
	}

	// 10. Wait for shutdown signal
	sig := <-sigCh
	logger.Info("收到退出信号，正在停止服务...", zap.String("signal", sig.String()))
	cancel()

	// 11. Wait for all workers to finish (with timeout)
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logger.Info("所有 worker 已正常停止")
	case <-time.After(30 * time.Second):
		logger.Error("等待 worker 停止超时(30s)，强制退出")
	}

	sqlDB.Close()
	logger.Info("服务已退出")
}

// runPrefixRotation 定期检查时间变化，自动切换表前缀
func runPrefixRotation(ctx context.Context, format string, db *gorm.DB, logger *zap.Logger) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			newPrefix := config.FormatStoragePrefix(format)
			currentPrefix := model.GetStoragePrefix()
			if newPrefix == currentPrefix {
				continue
			}
			rotateTables(currentPrefix, newPrefix, db, logger)
		}
	}
}

// rotateTables 切换表前缀：迁移游标和未完成的媒体任务到新表
func rotateTables(oldPrefix, newPrefix string, db *gorm.DB, logger *zap.Logger) {
	logger.Info("检测到时间变化，开始切换表前缀",
		zap.String("old_prefix", oldPrefix),
		zap.String("new_prefix", newPrefix))

	// 旧表显式表名（切换前缀后仍可通过显式表名访问）
	oldCursorTable := "corp_" + oldPrefix + "_seq_cursors"
	oldMediaTable := "corp_" + oldPrefix + "_media_tasks"

	// 1. 从旧表读取所有游标
	var cursors []model.CorpSeqCursor
	if err := db.Table(oldCursorTable).Find(&cursors).Error; err != nil {
		logger.Error("读取旧游标表失败", zap.Error(err))
		return
	}

	// 2. 从旧表读取未完成的媒体任务 (pending + downloading)
	var pendingTasks []model.MediaTask
	if err := db.Table(oldMediaTable).Where("status IN (0, 1)").Find(&pendingTasks).Error; err != nil {
		logger.Error("读取旧媒体任务表失败", zap.Error(err))
		return
	}

	// 3. 切换前缀（此后所有 TableName() 返回新表名）
	model.SetTablePrefix(newPrefix)

	// 4. 创建新表
	if err := model.AutoMigrate(db); err != nil {
		logger.Error("切表后建表失败", zap.Error(err))
		// 回滚前缀，避免 Worker 写入不存在的表
		model.SetTablePrefix(oldPrefix)
		return
	}

	// 5. 再从旧表读取一次游标（Worker 可能在步骤 1-3 之间更新了 seq）
	var latestCursors []model.CorpSeqCursor
	if err := db.Table(oldCursorTable).Find(&latestCursors).Error; err == nil {
		cursorMap := make(map[string]uint64)
		for _, c := range latestCursors {
			cursorMap[c.CorpName] = c.LastSeq
		}
		for i := range cursors {
			if latest, ok := cursorMap[cursors[i].CorpName]; ok && latest > cursors[i].LastSeq {
				cursors[i].LastSeq = latest
			}
		}
	}

	// 6. 迁移游标到新表
	for _, c := range cursors {
		c.ID = 0
		if err := db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "corp_name"}},
			DoUpdates: clause.AssignmentColumns([]string{"last_seq"}),
		}).Create(&c).Error; err != nil {
			logger.Error("迁移游标失败", zap.String("corp", c.CorpName), zap.Error(err))
		}
	}

	// 7. 迁移未完成的媒体任务到新表
	for i := range pendingTasks {
		pendingTasks[i].ID = 0
		pendingTasks[i].Status = 0 // 重置为 pending
	}
	if len(pendingTasks) > 0 {
		if err := db.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(pendingTasks, 100).Error; err != nil {
			logger.Error("迁移媒体任务失败", zap.Error(err))
		}
	}

	logger.Info("表前缀切换完成",
		zap.String("new_prefix", newPrefix),
		zap.Int("cursors_migrated", len(cursors)),
		zap.Int("media_tasks_migrated", len(pendingTasks)))
}

// migrateFromPreviousTable 启动时检查当前表是否有游标，
// 如果没有则从最近的旧 seq_cursors 表迁移游标和未完成的媒体任务。
func migrateFromPreviousTable(currentPrefix string, db *gorm.DB, logger *zap.Logger) {
	// 检查当前表是否已有游标
	var count int64
	db.Model(&model.CorpSeqCursor{}).Count(&count)
	if count > 0 {
		return // 当前表已有数据，无需迁移
	}

	// 查找所有 corp_*_seq_cursors 表，排除当前表
	currentTable := "corp_" + currentPrefix + "_seq_cursors"
	var tables []string
	db.Raw("SELECT TABLE_NAME FROM information_schema.TABLES WHERE TABLE_SCHEMA = DATABASE() AND TABLE_NAME LIKE 'corp_%_seq_cursors' AND TABLE_NAME != ? ORDER BY TABLE_NAME DESC", currentTable).Scan(&tables)

	if len(tables) == 0 {
		logger.Info("首次启动，无历史游标表")
		return
	}

	// 取最近的一张旧表（按表名倒序，时间最新的排最前）
	latestOldTable := tables[0]
	logger.Info("检测到当前表无游标，从旧表迁移",
		zap.String("source_table", latestOldTable),
		zap.String("target_table", currentTable))

	// 迁移游标
	var cursors []model.CorpSeqCursor
	if err := db.Table(latestOldTable).Find(&cursors).Error; err != nil {
		logger.Error("读取旧游标表失败", zap.String("table", latestOldTable), zap.Error(err))
		return
	}
	for _, c := range cursors {
		c.ID = 0
		if err := db.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "corp_name"}},
			DoUpdates: clause.AssignmentColumns([]string{"last_seq"}),
		}).Create(&c).Error; err != nil {
			logger.Error("迁移游标失败", zap.String("corp", c.CorpName), zap.Error(err))
		}
	}

	// 迁移未完成的媒体任务
	oldMediaTable := latestOldTable[:len(latestOldTable)-len("seq_cursors")] + "media_tasks"
	var pendingTasks []model.MediaTask
	if err := db.Table(oldMediaTable).Where("status IN (0, 1)").Find(&pendingTasks).Error; err != nil {
		logger.Warn("读取旧媒体任务表失败（可能不存在）", zap.Error(err))
	} else {
		for i := range pendingTasks {
			pendingTasks[i].ID = 0
			pendingTasks[i].Status = 0
		}
		if len(pendingTasks) > 0 {
			db.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(pendingTasks, 100)
		}
	}

	logger.Info("启动迁移完成",
		zap.Int("cursors_migrated", len(cursors)),
		zap.Int("media_tasks_migrated", len(pendingTasks)))
}

func initLogger(cfg config.LogConfig) (*zap.Logger, error) {
	level := zapcore.InfoLevel
	switch cfg.Level {
	case "debug":
		level = zapcore.DebugLevel
	case "warn":
		level = zapcore.WarnLevel
	case "error":
		level = zapcore.ErrorLevel
	}

	encoderCfg := zap.NewProductionEncoderConfig()
	encoderCfg.TimeKey = "time"
	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder

	var core zapcore.Core
	encoder := zapcore.NewJSONEncoder(encoderCfg)

	if cfg.File != "" {
		file, err := os.OpenFile(cfg.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return nil, fmt.Errorf("open log file: %w", err)
		}
		core = zapcore.NewCore(encoder, zapcore.AddSync(file), level)
	} else {
		core = zapcore.NewCore(encoder, zapcore.AddSync(os.Stdout), level)
	}

	return zap.New(core), nil
}
