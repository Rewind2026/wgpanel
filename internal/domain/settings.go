package domain

import "time"

// Settings 是可在运行时修改的面板设置。
//
// 与 config.Config 的分工：config.Config 是进程启动参数（数据目录、监听地址、
// 密钥来源），Settings 是运维可以在面板里改的策略项，改动立即生效、无需重启。
type Settings struct {
	// 采样间隔（秒）。限制范围 1 ~ 10，与 config 的 200ms 下限对应。
	SampleIntervalSeconds int `json:"sampleIntervalSeconds"`
	// 调谐间隔（秒）。限制范围 1 ~ 60。
	ReconcileIntervalSeconds int `json:"reconcileIntervalSeconds"`

	// 策略开关
	QuotaEnforcement  bool `json:"quotaEnforcement"`
	ExpiryEnforcement bool `json:"expiryEnforcement"`
	// 到期预警提前天数，0 表示不预警。
	ExpiryWarnDays int `json:"expiryWarnDays"`

	// 备份
	BackupEnabled        bool   `json:"backupEnabled"`
	BackupIntervalHours  int    `json:"backupIntervalHours"`
	BackupRetentionCount int    `json:"backupRetentionCount"`
	BackupDir            string `json:"backupDir"`

	// 出口地址显式覆盖。留空表示自动探测。
	PublicEndpoint string `json:"publicEndpoint"`

	UpdatedAt time.Time `json:"updatedAt"`
}

// DefaultSettings 返回内置默认设置。
func DefaultSettings() Settings {
	return Settings{
		SampleIntervalSeconds:    1,
		ReconcileIntervalSeconds: 2,
		QuotaEnforcement:         true,
		ExpiryEnforcement:        true,
		ExpiryWarnDays:           3,
		BackupEnabled:            true,
		BackupIntervalHours:      24,
		BackupRetentionCount:     7,
		BackupDir:                "backups",
		PublicEndpoint:           "",
	}
}

// Validate 校验设置取值范围。返回修正后的副本，保证落库值始终可用。
func (s Settings) Validate() (Settings, error) {
	out := s
	if out.SampleIntervalSeconds < 1 || out.SampleIntervalSeconds > 10 {
		return out, ErrInvalid("sampleIntervalSeconds must be between 1 and 10")
	}
	if out.ReconcileIntervalSeconds < 1 || out.ReconcileIntervalSeconds > 60 {
		return out, ErrInvalid("reconcileIntervalSeconds must be between 1 and 60")
	}
	if out.ExpiryWarnDays < 0 || out.ExpiryWarnDays > 90 {
		return out, ErrInvalid("expiryWarnDays must be between 0 and 90")
	}
	if out.BackupIntervalHours < 1 || out.BackupIntervalHours > 720 {
		return out, ErrInvalid("backupIntervalHours must be between 1 and 720")
	}
	if out.BackupRetentionCount < 1 || out.BackupRetentionCount > 100 {
		return out, ErrInvalid("backupRetentionCount must be between 1 and 100")
	}
	if out.BackupDir == "" {
		return out, ErrInvalid("backupDir must not be empty")
	}
	if out.BackupDir != filepath_Base(out.BackupDir) {
		return out, ErrInvalid("backupDir must be a plain directory name inside the data dir")
	}
	return out, nil
}

// filepath_Base 只取路径最后一段，用于阻止备份目录跳出数据目录。
// 放在 domain 层是因为「目录名必须干净」是业务约束，不只是字符串处理。
func filepath_Base(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' || p[i] == '\\' {
			return p[i+1:]
		}
	}
	if p == "." || p == ".." {
		return ""
	}
	return p
}
