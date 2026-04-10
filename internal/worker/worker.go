package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sync"
	"time"

	WeWorkFinanceSDK "github.com/NICEXAI/WeWorkFinanceSDK"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"wxworkChatData/internal/config"
	"wxworkChatData/internal/media"
	"wxworkChatData/internal/model"
)

type Worker struct {
	cfg          config.CorpConfig
	proxyCfg     config.ProxyConfig
	db           *gorm.DB
	pollClient   WeWorkFinanceSDK.Client // for GetChatData + DecryptData
	mediaClient  WeWorkFinanceSDK.Client // for GetMediaData (separate for thread safety)
	downloader   *media.Downloader
	logger       *zap.Logger
}

func NewWorker(cfg config.CorpConfig, proxyCfg config.ProxyConfig, mediaCfg config.MediaConfig,
	db *gorm.DB, logger *zap.Logger) (*Worker, error) {

	// Create two separate SDK clients for thread safety
	pollClient, err := WeWorkFinanceSDK.NewClient(cfg.CorpID, cfg.CorpSecret, cfg.RSAPrivateKey)
	if err != nil {
		return nil, fmt.Errorf("init poll SDK client for %s: %w", cfg.Name, err)
	}

	mediaClient, err := WeWorkFinanceSDK.NewClient(cfg.CorpID, cfg.CorpSecret, cfg.RSAPrivateKey)
	if err != nil {
		pollClient.Free()
		return nil, fmt.Errorf("init media SDK client for %s: %w", cfg.Name, err)
	}

	basePath := mediaCfg.BasePath
	if basePath == "" {
		basePath = "./media"
	}

	dl := media.NewDownloader(basePath, cfg.Name, mediaClient, proxyCfg, cfg.SDKTimeout, db, logger)

	return &Worker{
		cfg:         cfg,
		proxyCfg:    proxyCfg,
		db:          db,
		pollClient:  pollClient,
		mediaClient: mediaClient,
		downloader:  dl,
		logger:      logger.With(zap.String("corp", cfg.Name)),
	}, nil
}

// Run starts the polling loop and media downloader. Blocks until ctx is cancelled.
func (w *Worker) Run(ctx context.Context) {
	defer w.pollClient.Free()
	defer w.mediaClient.Free()

	var wg sync.WaitGroup

	// Start media downloader goroutine
	wg.Add(1)
	go func() {
		defer wg.Done()
		w.downloader.Run(ctx)
	}()

	// Main message polling loop
	w.logger.Info("archive worker started",
		zap.String("corp_id", w.cfg.CorpID),
		zap.Int("poll_interval", w.cfg.PollInterval),
		zap.Int("batch_size", w.cfg.BatchSize))

	seq, err := w.getLastSeq()
	if err != nil {
		w.logger.Error("加载 seq 游标失败，无法安全启动", zap.Error(err))
		wg.Wait()
		return
	}
	if seq > 0 {
		w.logger.Info("resuming from seq", zap.Uint64("seq", seq))
	}

	backoff := time.Second

	for {
		select {
		case <-ctx.Done():
			w.logger.Info("shutting down, waiting for media downloader...")
			wg.Wait()
			w.logger.Info("archive worker stopped")
			return
		default:
		}

		newSeq, count, err := w.pollOnce(ctx, seq)
		if err != nil {
			w.logger.Error("poll error", zap.Uint64("seq", seq), zap.Error(err))
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				wg.Wait()
				return
			}
			if backoff < 60*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second // reset on success

		w.logger.Debug("poll result", zap.Int("count", count), zap.Uint64("new_seq", newSeq))

		if count > 0 {
			seq = newSeq
			w.logger.Info("batch processed",
				zap.Int("count", count),
				zap.Uint64("new_seq", newSeq))
			continue // drain backlog immediately
		}

		// No new messages, sleep for poll interval
		select {
		case <-time.After(time.Duration(w.cfg.PollInterval) * time.Second):
		case <-ctx.Done():
			wg.Wait()
			return
		}
	}
}

func (w *Worker) pollOnce(ctx context.Context, currentSeq uint64) (newSeq uint64, count int, err error) {
	chatDataList, err := w.pollClient.GetChatData(
		currentSeq,
		uint64(w.cfg.BatchSize),
		w.proxyCfg.URL,
		w.proxyCfg.Password,
		w.cfg.SDKTimeout,
	)
	if err != nil {
		return currentSeq, 0, fmt.Errorf("GetChatData: %w", err)
	}

	if len(chatDataList) == 0 {
		return currentSeq, 0, nil
	}

	maxSeq := currentSeq
	var messages []model.Message
	var mediaTasks []model.MediaTask

	for _, chatData := range chatDataList {
		select {
		case <-ctx.Done():
			return maxSeq, count, nil
		default:
		}

		msg, mediaTask, err := w.decryptAndBuild(chatData)
		if err != nil {
			w.logger.Error("process message failed",
				zap.String("msg_id", chatData.MsgId),
				zap.Uint64("seq", chatData.Seq),
				zap.Error(err))
			continue
		}

		messages = append(messages, *msg)
		if mediaTask != nil {
			mediaTasks = append(mediaTasks, *mediaTask)
		}

		if chatData.Seq > maxSeq {
			maxSeq = chatData.Seq
		}
		count++
	}

	// Batch insert messages
	if len(messages) > 0 {
		if err := w.db.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(messages, 100).Error; err != nil {
			w.logger.Error("batch insert messages failed", zap.Error(err))
		}
	}

	// Batch insert media tasks
	if len(mediaTasks) > 0 {
		if err := w.db.Clauses(clause.OnConflict{DoNothing: true}).CreateInBatches(mediaTasks, 100).Error; err != nil {
			w.logger.Error("batch insert media tasks failed", zap.Error(err))
		}
	}

	// Persist seq cursor
	if maxSeq > currentSeq {
		if err := w.updateSeq(maxSeq); err != nil {
			w.logger.Error("update seq cursor failed", zap.Error(err))
		}
	}

	return maxSeq, count, nil
}

func (w *Worker) decryptAndBuild(chatData WeWorkFinanceSDK.ChatData) (*model.Message, *model.MediaTask, error) {
	// Decrypt message (empty string uses default private key)
	chatMsg, err := w.pollClient.DecryptData(chatData.EncryptRandomKey, chatData.EncryptChatMsg, "")
	if err != nil {
		return nil, nil, fmt.Errorf("decrypt: %w", err)
	}

	rawJSON := chatMsg.GetRawChatMessage()

	// Parse additional fields from raw JSON
	var extra struct {
		RoomID  string `json:"roomid"`
		MsgTime int64  `json:"msgtime"`
	}
	if err := json.Unmarshal(rawJSON, &extra); err != nil {
		w.logger.Warn("unmarshal extra fields failed",
			zap.String("msg_id", chatMsg.Id),
			zap.Error(err))
	}

	toListJSON, _ := json.Marshal(chatMsg.ToList)

	msg := &model.Message{
		CorpName:     w.cfg.Name,
		MsgID:        chatMsg.Id,
		Seq:          chatData.Seq,
		Action:       chatMsg.Action,
		FromUser:     chatMsg.From,
		ToList:       toListJSON,
		RoomID:       extra.RoomID,
		MsgType:      chatMsg.Type,
		MsgTime:      extra.MsgTime,
		Content:      rawJSON,
		PublickeyVer: chatData.PublickeyVer,
	}

	mediaTask := w.extractMediaTask(chatMsg, extra.MsgTime)
	return msg, mediaTask, nil
}

func (w *Worker) extractMediaTask(chatMsg WeWorkFinanceSDK.ChatMessage, msgTime int64) *model.MediaTask {
	var sdkFileID, md5sum, fileExt string
	var fileSize uint64

	switch chatMsg.Type {
	case "image":
		m := chatMsg.GetImageMessage()
		sdkFileID = m.Image.SdkFileID
		md5sum = m.Image.Md5Sum
		fileSize = uint64(m.Image.FileSize)
		fileExt = "jpg"
	case "voice":
		m := chatMsg.GetVoiceMessage()
		sdkFileID = m.Voice.SdkFileID
		md5sum = m.Voice.Md5Sum
		fileSize = uint64(m.Voice.VoiceSize)
		fileExt = "amr"
	case "video":
		m := chatMsg.GetVideoMessage()
		sdkFileID = m.Video.SdkFileID
		md5sum = m.Video.Md5Sum
		fileSize = uint64(m.Video.FileSize)
		fileExt = "mp4"
	case "emotion":
		m := chatMsg.GetEmotionMessage()
		sdkFileID = m.Emotion.SdkFileID
		md5sum = m.Emotion.Md5Sum
		fileSize = uint64(m.Emotion.ImageSize)
		if m.Emotion.Type == 1 {
			fileExt = "gif"
		} else {
			fileExt = "png"
		}
	case "file":
		m := chatMsg.GetFileMessage()
		sdkFileID = m.File.SdkFileID
		md5sum = m.File.Md5Sum
		fileSize = uint64(m.File.FileSize)
		fileExt = m.File.FileExt
		if fileExt == "" {
			fileExt = filepath.Ext(m.File.FileName)
			if len(fileExt) > 0 && fileExt[0] == '.' {
				fileExt = fileExt[1:]
			}
		}
	case "meeting_voice_call":
		m := chatMsg.GetMeetingVoiceCallMessage()
		sdkFileID = m.MeetingVoiceCall.SdkFileID
		fileExt = "amr"
	case "voip_doc_share":
		m := chatMsg.GetVoipDocShareMessage()
		sdkFileID = m.VoipDocShare.SdkFileID
		md5sum = m.VoipDocShare.Md5Sum
		fileSize = m.VoipDocShare.FileSize
		fileExt = filepath.Ext(m.VoipDocShare.FileName)
		if len(fileExt) > 0 && fileExt[0] == '.' {
			fileExt = fileExt[1:]
		}
	default:
		return nil
	}

	if sdkFileID == "" {
		return nil
	}

	if fileExt == "" {
		fileExt = "bin"
	}

	return &model.MediaTask{
		CorpName:  w.cfg.Name,
		MsgID:     chatMsg.Id,
		MsgType:   chatMsg.Type,
		SdkFileID: sdkFileID,
		Md5sum:    md5sum,
		FileSize:  fileSize,
		FileExt:   fileExt,
		MsgTime:   msgTime,
	}
}

func (w *Worker) getLastSeq() (uint64, error) {
	var cursor model.CorpSeqCursor
	err := w.db.Where("corp_name = ?", w.cfg.Name).First(&cursor).Error
	if err == gorm.ErrRecordNotFound {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return cursor.LastSeq, nil
}

func (w *Worker) updateSeq(seq uint64) error {
	cursor := model.CorpSeqCursor{
		CorpName: w.cfg.Name,
		LastSeq:  seq,
	}
	return w.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "corp_name"}},
		DoUpdates: clause.AssignmentColumns([]string{"last_seq"}),
	}).Create(&cursor).Error
}
