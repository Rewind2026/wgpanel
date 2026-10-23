//go:build !race

package httpapi

// raceDetector 标记当前测试二进制是否由 -race 构建。
// 详见 race_on_test.go。
const raceDetector = false
