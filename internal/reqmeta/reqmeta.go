// Package reqmeta 携带「这次操作来自哪个 HTTP 请求」的来源信息。
//
// 审计要记录来源 IP 与 User-Agent，但 AuditEvent 是在 service 层构造的，
// 而 *http.Request 止步于 handler 层：给每个 service 方法都加上这两个参数
// 会让签名膨胀到没人愿意补全——实测就是这么漏掉的，287 条登录成功记录
// 的 ip/ua 全为空。
//
// 这里改成由 HTTP 中间件注入 context，仓储层写审计时统一补齐：
// 新增审计点不必记得填，请求来源也只有这一个入口。
package reqmeta

import "context"

type ctxKey struct{}

// Info 是一次请求的来源信息。零值表示「无 HTTP 来源」，
// 例如定时任务触发的策略执行，IP 与 UA 本来就无从谈起。
type Info struct {
	IP        string
	UserAgent string
}

// With 把请求来源写入 context。
func With(ctx context.Context, i Info) context.Context {
	return context.WithValue(ctx, ctxKey{}, i)
}

// From 取出请求来源；不在 HTTP 上下文中时返回零值。
func From(ctx context.Context) Info {
	i, _ := ctx.Value(ctxKey{}).(Info)
	return i
}

// FillIn 用请求来源补齐审计事件中缺失的字段。
//
// 只补空位，不覆盖已填的值：登录限流这类场景能从请求里拿到更权威的
// 来源（带 XFF 解析），不必被后注入的值改写。
func FillIn(ctx context.Context, ip, ua *string) {
	i := From(ctx)
	if *ip == "" {
		*ip = i.IP
	}
	if *ua == "" {
		*ua = i.UserAgent
	}
}
