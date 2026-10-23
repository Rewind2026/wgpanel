package store

import (
	"context"
	"fmt"
)

// DumpableTables 是允许逻辑导出的表白名单。
// 表名会拼进 SQL，所以必须在 store 层自己校验，
// 不能只依赖调用方（备份服务）的判断。
var DumpableTables = map[string]bool{
	"server_config": true,
	"peer":          true,
	"user":          true,
	"app_setting":   true,
}

// DumpTable 按「列名 + 按列有序的值」导出一张表。
//
// 为什么不做「对象 → JSON → 对象」：领域模型带 omitempty 与 json:"-"，
// 私钥与密码哈希会在往返中消失，恢复后所有用户都登不进、客户端配置全废。
// 这里搬运列的原始值，NULL 也原样保留。
//
// 用只读连接池导出：读不阻塞写，导出期间面板照常可用；
// 写连接只有一条，因此导出的多张表之间不是一个事务快照——
// 对配置备份来说可以接受，恢复语义本就是「回到某个时刻的配置」。
func (s *Store) DumpTable(ctx context.Context, table string) (columns []string, rows [][]any, err error) {
	if !DumpableTables[table] {
		return nil, nil, fmt.Errorf("store: table %q is not exportable", table)
	}

	r, err := s.ro.QueryContext(ctx, `SELECT * FROM `+table)
	if err != nil {
		return nil, nil, fmt.Errorf("store: dump %s: %w", table, err)
	}
	defer r.Close()

	columns, err = r.Columns()
	if err != nil {
		return nil, nil, fmt.Errorf("store: dump %s columns: %w", table, err)
	}

	rows = make([][]any, 0, 64)
	for r.Next() {
		values := make([]any, len(columns))
		holders := make([]any, len(columns))
		for i := range values {
			holders[i] = &values[i]
		}
		if err := r.Scan(holders...); err != nil {
			return nil, nil, fmt.Errorf("store: dump %s scan: %w", table, err)
		}
		rows = append(rows, values)
	}
	return columns, rows, r.Err()
}
