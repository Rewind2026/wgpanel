//go:build linux

package netdriver

// Platform 返回当前平台的 netdriver 实现。
func Platform() NetDriver { return New() }
