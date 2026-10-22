package sqlscript

import (
	"slices"

	"github.com/xwb1989/sqlparser"
)

// InferSQLPhase 仅解析 SQL 正文确定展示类型，不读取或信任来源目录名称。
// 参数 data：SQL 记录字段，只使用 sql 正文；phase、source_path 等标签均不参与分类。
// 返回：创建表、修改表、初始化数据或待识别；混合脚本采用最高优先级，内部语句不重排。
func InferSQLPhase(data map[string]string) string {
	// AI 填写的 phase 和源文件归档位置完全不参与判断，正文变化后重新计算。
	// 按词法拆分真实语句，字符串、注释中的 CREATE TABLE 不会误导分类。
	// 目录即使叫“新增表”，其中的 UPDATE 脚本仍按初始化数据显示。
	statements, err := sqlparser.SplitStatementToPieces(data["sql"])
	if err != nil {
		return "待识别"
	}
	rank := len(SQLPhases) - 1
	for _, statement := range statements {
		tokens, err := ReadSQLTokens(statement)
		if err != nil {
			return "待识别"
		}
		// 混合脚本取最早依赖的类型，但绝不调整作者原始 SQL 的执行顺序。
		// 创建优先级高于修改，修改优先级高于普通数据语句。
		if candidate := StatementPhase(tokens); candidate < rank {
			rank = candidate
		}
	}
	return SQLPhases[rank]
}

// StatementPhase 从单条语句的真实首部命令得到类型优先级。
// 参数 tokens：去掉字符串和注释后的词法单元，不能由目录名或文件名生成。
// 返回：0 创建表，1 结构修改，2 数据/会话初始化，3 待识别。
func StatementPhase(tokens []SQLToken) int {
	// 返回的是有限排序等级，不是 SQL 的执行结果或安全许可。
	// 索引创建属于结构修改，不与 CREATE TABLE 混为同一种操作。
	// 空白和纯注释不参与类型决策；临时表仍属于建表操作。
	// 首部判断不会把 SELECT 字符串或后面的字段名当作 SQL 命令。
	if len(tokens) == 0 {
		return 3
	}
	first := tokens[0].Text
	if first == "create" && len(tokens) > 1 {
		if tokens[1].Text == "table" || (tokens[1].Text == "temporary" && len(tokens) > 2 && tokens[2].Text == "table") {
			return 0
		}
		return 1
	}
	// 索引、重命名、删除等结构操作都排在初始化数据之前。
	// 分类只用于展示，不表示该语句已经获得执行授权。
	if slices.Contains([]string{"alter", "drop", "rename"}, first) {
		return 1
	}
	if slices.Contains([]string{"insert", "update", "delete", "replace", "select", "set", "truncate"}, first) {
		return 2
	}
	return 3
}
