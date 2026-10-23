package scheduler

import (
	"context"
	"log/slog"
	"time"

	"github.com/Rewind2026/wgpanel/internal/domain"
	"github.com/Rewind2026/wgpanel/internal/store"
)

// backupTick 是检查「是否该生成定时备份」的节奏。
//
// 不按 BackupIntervalHours 直接建 ticker：那个值存在数据库里、可被用户随时改，
// 改了之后已建立的 ticker 不会跟着变。这里按固定短周期检查一次到期时间，
// 设置改动最迟一拍生效，代价是每分钟多一次几乎空转的读库。
const backupTick = time.Minute

// BackupCreator 由 *backup.Service 实现，接口化以便 scheduler 不依赖备份实现。
type BackupCreator interface {
	Create(ctx context.Context, kind string, operatorID *int64) (*domain.BackupRecord, error)
}

// StartBackupLoop 按时钟生成备份，直到 ctx 取消。
//
// last 记录上一次备份时间。初始为零值表示「还没打过备份」，
// 但启动时并不立刻补一份：面板刚起来、schema 可能还在迁移，
// 等到第一个周期边界再备份更安全。
func StartBackupLoop(ctx context.Context, settings *store.SettingRepo, b BackupCreator,
	publish func(domain.Event), log *slog.Logger) {
	if b == nil || settings == nil {
		return
	}

	go func() {
		ticker := time.NewTicker(backupTick)
		defer ticker.Stop()

		log.Info("scheduled backup loop started")
		var last time.Time
		for {
			select {
			case <-ctx.Done():
				log.Info("scheduled backup loop stopped")
				return
			case <-ticker.C:
				cfg, err := settings.LoadSettings(ctx)
				if err != nil {
					log.Error("load settings for backup failed", "error", err)
					continue
				}
				if !cfg.BackupEnabled {
					last = time.Time{}
					continue
				}
				now := time.Now()
				interval := time.Duration(cfg.BackupIntervalHours) * time.Hour
				if !last.IsZero() && now.Sub(last) < interval {
					continue
				}
				// 先占位再执行：Create 失败时保持 last 不变，下一拍会重试；
				// 成功则把 last 推到现在，形成固定节奏。
				rec, err := b.Create(ctx, "scheduled", nil)
				if err != nil {
					log.Error("scheduled backup failed", "error", err)
					continue
				}
				last = now
				log.Info("scheduled backup created", "filename", rec.Filename, "bytes", rec.SizeBytes)
				if publish != nil {
					publish(domain.Event{
						Type:       domain.EventBackupCreated,
						Message:    "定时备份已创建：" + rec.Filename,
						OccurredAt: now,
					})
				}
			}
		}
	}()
}
