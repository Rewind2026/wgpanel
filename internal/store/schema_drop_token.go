package store

// schemaV9 删除 API 令牌表。
//
// 令牌功能整体移除：单机部署没有调用 API 的第三方系统，
// 留着这张表只会让人以为还能签发凭据。
const schemaV9 = `
DROP TABLE IF EXISTS api_token;
`
