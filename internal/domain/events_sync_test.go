package domain

import (
	"os"
	"regexp"
	"testing"
)

// 前端事件清单（web/src/types/api.ts 的 DOMAIN_EVENT_TYPES）必须与后端
// AllEventTypes() 完全一致。
//
// 真机上暴露的缺陷：后端加了 peer.created / user.created 等 8 个事件并发布了，
// 但前端 SSE 监听器仍写着9 项的旧清单。
// EventSource 对未注册具名监听器的事件不报错，界面也毫无异常——
// 事件只是静默消失。跨语言的两份清单没有编译器兜底，只能靠测试钉住。
func TestFrontendEventListMatchesBackend(t *testing.T) {
	raw, err := os.ReadFile("../../web/src/types/api.ts")
	if err != nil {
		t.Fatalf("read frontend types: %v", err)
	}
	// 只取 DOMAIN_EVENT_TYPES 数组体，避免误抓 DomainEventType 联合类型里的名字。
	block := regexp.MustCompile(`(?s)export const DOMAIN_EVENT_TYPES.*?= \[(.*?)\] as const`)
	m := block.FindSubmatch(raw)
	if m == nil {
		t.Fatal("web/src/types/api.ts 里找不到 DOMAIN_EVENT_TYPES 数组")
	}
	item := regexp.MustCompile(`'([a-z_.]+)'`)
	frontend := map[string]bool{}
	for _, g := range item.FindAllStringSubmatch(string(m[1]), -1) {
		frontend[g[1]] = true
	}

	for _, ev := range AllEventTypes() {
		if !frontend[string(ev)] {
			t.Errorf("前端事件清单缺少 %s：后端会推送，但浏览器没有监听器", ev)
		}
	}
	// 反向也查一遍：前端多写一个名字，订阅它就永远收不到东西。
	backend := map[string]bool{}
	for _, ev := range AllEventTypes() {
		backend[string(ev)] = true
	}
	itemAll := regexp.MustCompile(`'([a-z][a-z_.]+)'`)
	for _, g := range itemAll.FindAllStringSubmatch(string(block.FindSubmatch(raw)[1]), -1) {
		if !backend[g[1]] {
			t.Errorf("前端事件清单含后端不存在的事件 %s", g[1])
		}
	}
}

// 事件名清单不允许重复：重复项会让同一事件被推送两遍，
// 而界面上看不出任何异常。
func TestEventTypesAreUnique(t *testing.T) {
	seen := map[EventType]bool{}
	for _, ev := range AllEventTypes() {
		if seen[ev] {
			t.Errorf("事件 %s 在清单里出现两次", ev)
		}
		seen[ev] = true
	}
}
