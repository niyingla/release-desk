package schema

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

type Access interface {
	Read(context.Context, string) (Table, error)
	Exec(context.Context, string) (int64, error)
}
type TxAccess struct{ Tx *sql.Tx }

var updateTime = regexp.MustCompile(`(?i)on update\s+([^ ]+)`)

// 元数据列的排序规则可能忽略大小写；按服务器表名规则避免把 Foo 与 foo 误判成同一表。
const tableScope = "TABLE_SCHEMA=DATABASE() AND TABLE_NAME=? AND (@@lower_case_table_names<>0 OR BINARY TABLE_NAME=BINARY ?)"

// Read 查询当前连接库的真实表、字段和索引，不信任历史执行状态。
// 参数 ctx：本轮可取消、有限时的上下文；参数 name：解析出的单表名。
// 返回：实时结构；元数据权限、连接或查询错误时不假称对象不存在。
func (a TxAccess) Read(ctx context.Context, name string) (Table, error) {
	// DATABASE() 绑定已核对的目标连接，不把库名从 SQL 原文带入查询。
	// 只有明确的无行结果才代表表不存在，权限错误必须单独反馈。
	// 视图不能冒充可执行字段修改的普通表。
	// 字段与索引都要读取成功，才能提供完整的结构快照。
	// 表名作为参数，数据库范围由 DATABASE() 固定到当前目标连接。
	result := Table{Columns: map[string]Column{}, Indexes: map[string]Index{}}
	err := a.Tx.QueryRowContext(ctx, "SELECT ENGINE,TABLE_COLLATION,TABLE_COMMENT FROM information_schema.TABLES WHERE "+tableScope+" AND TABLE_TYPE='BASE TABLE'", name, name).Scan(&result.Engine, &result.Collation, &result.Comment)
	if err == sql.ErrNoRows {
		return result, nil
	}
	if err != nil {
		return result, fmt.Errorf("无法读取表结构：%w", err)
	}
	result.Exists = true
	result.Charset = strings.Split(result.Collation, "_")[0]
	// 只有确认表存在后才查字段；拒绝把查询失败当作空表结构。
	if err = a.readColumns(ctx, name, &result); err != nil {
		return result, err
	}
	if err = a.readIndexes(ctx, name, &result); err != nil {
		return result, err
	}
	return result, nil
}

// readColumns 读取完整字段属性用于定义比较。
// 参数 ctx：核验上下文；参数 name：当前目标表；参数 table：本次独立快照。
// 返回：查询或扫描错误，失败不会报告部分字段为完整核验。
func (a TxAccess) readColumns(ctx context.Context, name string, table *Table) error {
	// 默认值的 NULL 与空字符串分别保留，避免虚假匹配。
	// 字段类型使用 COLUMN_TYPE，包含长度、精度、符号及枚举值。
	// 额外属性中的自动增长影响写入行为，必须纳入检查。
	// 自动更新时间单独提取，不能与默认生成标志混淆。
	// 字段字符集为空通常意味着非字符类型，不猜测为数据库默认值。
	// 列名比较按 MySQL 列名语义归一，不改动其业务默认值。
	// 逐行扫描错误会使整次核验失败，不能使用不完整的字段集合。
	// 使用元数据原值，不从 SHOW CREATE 文本或错误信息猜测字段是否存在。
	rows, err := a.Tx.QueryContext(ctx, "SELECT COLUMN_NAME,COLUMN_TYPE,IS_NULLABLE,COLUMN_DEFAULT,EXTRA,COLUMN_COMMENT,CHARACTER_SET_NAME,COLLATION_NAME,ORDINAL_POSITION FROM information_schema.COLUMNS WHERE "+tableScope+" ORDER BY ORDINAL_POSITION", name, name)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var column Column
		var columnName, nullable, extra string
		var def, charset, collation sql.NullString
		if err = rows.Scan(&columnName, &column.Type, &nullable, &def, &extra, &column.Comment, &charset, &collation, &column.Ordinal); err != nil {
			return err
		}
		// NULL 默认值与空字符串分别保留，避免错误判断初始化定义。
		column.Nullable = nullable == "YES"
		if def.Valid {
			value := def.String
			column.Default = &value
		}
		column.AutoIncrement = strings.Contains(strings.ToLower(extra), "auto_increment")
		column.Charset = charset.String
		column.Collation = collation.String
		if matches := updateTime.FindStringSubmatch(extra); len(matches) > 1 {
			column.OnUpdate = normalizeTemporal(matches[1])
		}
		table.Columns[strings.ToLower(columnName)] = column
	}
	return rows.Err()
}

// readIndexes 读取索引的唯一性、列顺序及前缀长度。
// 参数 ctx：核验上下文；参数 name：当前目标表；参数 table：本次独立快照。
// 返回：查询或扫描错误；函数索引无法作为普通列索引匹配。
func (a TxAccess) readIndexes(ctx context.Context, name string, table *Table) error {
	// 索引的每一列都按 SEQ_IN_INDEX 顺序追加，不能按列名字母排序。
	// NON_UNIQUE 决定唯一约束，不从索引名称推测。
	// 列前缀长度参与定义，完整列索引与前缀索引并不相同。
	// 函数索引没有普通列名，需要保留未知表达式标志。
	// 索引方法可能不同，不能把 HASH 与 BTREE 视为同义。
	// 读取失败不会把实际索引误判为不存在。
	// 多行索引必须完整读取，不能仅取第一列就结束。
	// 查询参数始终只表示表名，不拼接到 SQL 语法中。
	// 按索引和列序号排序，复合索引不同顺序不能被视为同一个定义。
	rows, err := a.Tx.QueryContext(ctx, "SELECT INDEX_NAME,NON_UNIQUE,COLUMN_NAME,SUB_PART,INDEX_TYPE FROM information_schema.STATISTICS WHERE "+tableScope+" ORDER BY INDEX_NAME,SEQ_IN_INDEX", name, name)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var indexName, method string
		var nonUnique int
		var column sql.NullString
		var prefix sql.NullInt64
		if err = rows.Scan(&indexName, &nonUnique, &column, &prefix, &method); err != nil {
			return err
		}
		// 表达式索引列名为空，明确保留未知标志，不能与普通列名巧合匹配。
		key := strings.ToLower(indexName)
		index := table.Indexes[key]
		index.Unique = nonUnique == 0
		index.Method = method
		value := "<表达式索引>"
		if column.Valid {
			value = strings.ToLower(column.String)
		}
		if prefix.Valid {
			value += "(" + strconv.FormatInt(prefix.Int64, 10) + ")"
		}
		index.Columns = append(index.Columns, value)
		table.Indexes[key] = index
	}
	return rows.Err()
}

// Exec 通过当前单库事务执行已经核对的语句。
// 参数 ctx：同一轮取消上下文；参数 source：原文或仅去掉已存在子句的等价 ALTER。
// 返回：影响行数和服务端错误；执行成功仍需另行查询真实结构。
func (a TxAccess) Exec(ctx context.Context, source string) (int64, error) {
	// 不启用多语句模式，每次调用仍只执行一条已经拆分的语句。
	result, err := a.Tx.ExecContext(ctx, source)
	if err != nil {
		return 0, err
	}
	// 影响行数只作执行摘要，不能代替 DDL 结构核验。
	return result.RowsAffected()
}
