//go:build !linux

package netdriver

// Platform 返回当前平台的 netdriver 实现。
// 非 Linux 平台（开发机、CI、文档构建）返回空实现，避免阻塞本地开发。
func Platform() NetDriver { return NewStub() }
