package version

// 由 ldflags 在构建时注入。
var (
	Version   = "dev"
	Commit    = "none"
	BuildDate = "unknown"
)

// Info 汇总构建信息。
type Info struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	BuildDate string `json:"buildDate"`
}

// Get 返回当前构建信息。
func Get() Info {
	return Info{Version: Version, Commit: Commit, BuildDate: BuildDate}
}
