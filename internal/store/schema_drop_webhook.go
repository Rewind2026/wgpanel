package store

// schemaV10 删除通知出口表。
//
// Webhook 功能整体下线：单机部署没有第二个消费方，往外部系统推送事件
// 需要面板能出网，而部署环境通常没有外网出口——实测投递全部超时失败。
// 留着这张表只会让人以为还能配通知。SSE 实时推送走的是另一条通道
// （eventbus 的订阅通道），不受影响。
const schemaV10 = `
DROP TABLE IF EXISTS webhook;
`
