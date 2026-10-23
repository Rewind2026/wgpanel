//go:build race

package httpapi

// raceDetector 标记当前测试二进制是否由 -race 构建。
//
// 压测用例用它决定要不要断言时延目标：竞态检测器给每一次内存访问插桩，
// 实测把接口延迟放大 10~50 倍（/api/v1/peers 的 P99 从 8ms 变成 426ms）。
// 在插桩下断言 30ms 的 P99 目标没有意义——那测的是检测器，不是产品。
// 正确性断言（状态码、条数、字段完整性）不受影响，照常执行。
const raceDetector = true
