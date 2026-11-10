package store

// NormalizeUsernameCaseSQL 把 user 表里的用户名统一成小写。
//
// 用户名不区分大小写，建号与登录都按小写规范形式处理。
// 但旧库里的用户名是原样落库的：首个 owner 来自 WGP_ADMIN_USERNAME，
// 写成 "Admin" 库里就存着 "Admin"。
//
// 不归一的后果不是「不好看」，而是审计日志里同一个账号出现两种写法
// （「Admin」和「admin」），「谁改的」这类问题就永远说不清。
//
// 只归一没有大小写冲突的行：若库里真存在 "Admin" 与 "admin" 两行
// （只能来自手工改库或从更老的备份恢复），归一它们会撞上 UNIQUE 约束，
// 整条语句失败、面板直接起不来。冲突组宁可保持原样——
// 那种状态下 GetByUsername 会明确报歧义，而不是猜一个身份给出去。
//
// lower() 只折叠 ASCII，与用户名允许的字符集（字母、数字、_ . -）一致，
// 也与 domain.CanonicalUsername 的口径一致。
const NormalizeUsernameCaseSQL = `UPDATE user
   SET username = lower(username)
 WHERE username <> lower(username)
   AND (SELECT COUNT(*) FROM user u2 WHERE lower(u2.username) = lower(user.username)) = 1;`

// schemaV11 把存量用户名统一成小写。
const schemaV11 = NormalizeUsernameCaseSQL
