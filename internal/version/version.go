// Package version 保存构建时注入的版本信息，由 Makefile 通过 -ldflags 写入。
package version

var (
	// Version 语义化版本或 git describe 结果。
	Version = "dev"
	// GitCommit 构建对应提交。
	GitCommit = "unknown"
)

// String 返回 "version (commit)" 形式的展示串。
func String() string {
	if GitCommit == "unknown" || GitCommit == "" {
		return Version
	}
	return Version + " (" + GitCommit + ")"
}
