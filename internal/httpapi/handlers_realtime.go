package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// eventFrame 是 WebSocket 推送的统一帧格式。
type eventFrame struct {
	Type    string `json:"type"`
	Payload any    `json:"payload"`
	SentAt  int64  `json:"sentAt"`
}

// 推送消息类型常量。
const (
	EventMetrics     = "metrics"
	EventPeerOnline  = "peer.online"
	EventPeerOffline = "peer.offline"
	EventServer      = "server.updated"
	EventHello       = "hello"
)

// handleRealtime 通过 SSE 推送监控快照与领域事件。
// 选择 SSE 而非 WebSocket 的原因：服务端单向推送、自动重连、代理友好，
// 且监控面板的数据全部来自服务端，不需要客户端上行消息。
func (s *Server) handleRealtime(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		respondError(w, s.log, fmt.Errorf("streaming unsupported"))
		return
	}

	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// 先订阅再发首帧。顺序反了的话，客户端收到 hello 到服务端真正
	// 进入 select 循环之间存在一个空窗，这段时间发布的事件既不会被
	// 缓冲也不会被感知——SSE 客户端把 hello 当作「流已就绪」的信号，
	// 这个信号必须名副其实。订阅在前时该空窗内的事件会堆在带缓冲的
	// channel 里，首帧之后照常送达。
	metricsCh, cancelMetrics := s.collector.Subscribe()
	defer cancelMetrics()

	// 领域事件（上下线、配额、备份）与指标走同一条连接：
	// 前端只需要一个 EventSource，重连与鉴权逻辑也只有一处。
	eventCh, cancelEvents := s.events.Subscribe()
	defer cancelEvents()

	// 首帧立即下发，避免客户端空窗。
	if err := writeSSE(w, eventFrame{Type: EventHello, Payload: map[string]any{
		"interval": s.cfg.SampleInterval.String(),
	}, SentAt: time.Now().UnixMilli()}); err != nil {
		return
	}
	flusher.Flush()

	keepalive := time.NewTicker(20 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case snap, open := <-metricsCh:
			if !open {
				return
			}
			if err := writeSSE(w, eventFrame{
				Type:    EventMetrics,
				Payload: snap,
				SentAt:  time.Now().UnixMilli(),
			}); err != nil {
				return
			}
			flusher.Flush()
		case ev, open := <-eventCh:
			if !open {
				return
			}
			if err := writeSSE(w, eventFrame{
				Type:    string(ev.Type),
				Payload: ev,
				SentAt:  time.Now().UnixMilli(),
			}); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeSSE 写出一帧 SSE。
func writeSSE(w http.ResponseWriter, frame eventFrame) error {
	payload, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", frame.Type, payload)
	return err
}
