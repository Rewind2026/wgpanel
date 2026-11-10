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

// BackupLister 提供备份索引的读取能力。
//
// 由 *store.BackupRepo 实现。scheduler 靠它拿到「上一次定时备份的时间」，
// 使备份节奏跨进程重启延续，而不是每次重启都重新开始计时。
type BackupLister interface {
	List(ctx context.Context) ([]domain.BackupRecord, error)
}

// lastScheduledTime 返回索引中最近一次「定时备份」的时间。
//
// 取不到任何记录时返回零值：调用方据此判断「这是第一次备份」。
func lastScheduledTime(ctx context.Context, l BackupLister) time.Time {
	if l == nil {
		return time.Time{}
	}
	records, err := l.List(ctx)
	if err != nil {
		return time.Time{}
	}
	var newest time.Time
	for _, rec := range records {
		if rec.Kind != "scheduled" {
			continue
		}
		t := rec.CreatedAt
		if t.After(newest) {
			newest = t
		}
	}
	return newest
}

// StartBackupLoop 按时钟生成备份，直到 ctx 取消。
//
// 节奏基准取「上一次定时备份的时间」，从备份索引里读，
// 而不是记在进程内的变量里：
//
//   - 记在变量里的话，容器每次重启都归零，重启后的第一拍必然立刻打一份
//     「定时」备份。这与「启动时不立刻补一份」的意图正好相反，
//     而且保留窗口只有 BackupRetentionCount 份——频繁重启会让这些
//     重启备份挤掉真正按周期产生的备份。
//   - 从索引读则跨重启延续：重启后若距上次已超过间隔，就补一份；
//     没到时间就继续等，不会因为重启而多打。
func StartBackupLoop(ctx context.Context, settings *store.SettingRepo, b BackupCreator,
	list BackupLister, publish func(domain.Event), log *slog.Logger) {
	if b == nil || settings == nil {
		return
	}

	go func() {
		ticker := time.NewTicker(backupTick)
		defer ticker.Stop()

		log.Info("scheduled backup loop started")
		// 节奏基准来自备份索引，跨进程重启延续。
		last := lastScheduledTime(ctx, list)
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
					continue
				}
				now := time.Now()
				interval := time.Duration(cfg.BackupIntervalHours) * time.Hour
				// 从未打过定时备份（索引为空）：对齐基准后等下一个周期，
				// 不在启动后立刻补一份。
				if last.IsZero() {
					last = now
					continue
				}
				if now.Sub(last) < interval {
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
