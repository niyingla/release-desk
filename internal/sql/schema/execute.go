package schema

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// Run 执行脚本并核对最终目标结构，支持只复核历史已成功目标。
// 参数 ctx：本轮单库上下文；参数 statements：保留作者顺序的语句。
// 参数 access：同一目标库的元数据与执行适配器；参数 checkOnly：只查结构，不再执行数据语句。
// 返回：执行和核验摘要；不一致、无法核验或 SQL 错误不会返回核验通过。
func Run(ctx context.Context, statements []string, access Access, checkOnly bool) (string, error) {
	// 最终约束汇总跨越整份脚本，避免把中间态当成最终目标。
	// 历史成功的脚本复核时不再重放 INSERT、UPDATE 等数据语句。
	// 只读复核遇到未知语法时直接要求人工核验，不猜测结构。
	// 正常执行保持作者的语句顺序，子句省略只针对已解析的普通 ALTER。
	// 每个新语句之前检查取消，停止后不继续下一个外部写操作。
	// 非结构语句不会因为某个表已存在而被跳过。
	// 部分字段已存在时只发送仍需变更的已支持子句。
	// 旧表存在可以免除重复 CREATE，但不能免除最终字段与索引检查。
	// 任何普通 SQL 错误立即结束当前库，其他目标库由上层继续。
	// 影响行数仅用于执行摘要，不参与结构核验结论。
	// 全部语句处理后重新读取真实元数据，避免相信执行返回值。
	// 有任何结构差异时不能生成成功凭证。
	// 未知结构扩展始终显示待核验，即使 Exec 没有报错。
	// 先建立全部最终约束，后续 MODIFY 同字段时以最后一次定义为准。
	operations := make([]Operation, len(statements))
	checks := []Check{}
	unknownReasons := []string{}
	for i, source := range statements {
		operations[i] = Parse(source)
		op := operations[i]
		if op.Structural && !op.Supported {
			unknownReasons = append(unknownReasons, op.Reason)
		}
		for _, clause := range op.Clauses {
			checks = append(checks, clause.Checks...)
		}
	}
	checks = finalChecks(checks)
	// 目标定义可被后续同名修改覆盖，不能拿中间态阻止已成功库的只读复核。
	if checkOnly {
		// 成功历史只能免除重复数据写入，不能免除实际结构查询。
		if len(checks) == 0 || len(unknownReasons) > 0 {
			return "", &VerificationError{Message: "历史已成功库仅复核：包含无法完整自动核验的结构语句，请人工核对", Unverified: true}
		}
		if err := verify(ctx, access, checks); err != nil {
			return "", err
		}
		return "只读结构核验通过 · 已重新查询实际结构 · 未执行 DDL 或数据语句", nil
	}
	var affected int64
	appliedChecks := []Check{}
	// 执行中的约束按原顺序累计，允许后续 ALTER 修正旧表，但先阻止错误结构上的数据写入。
	unverifiedDDL := false
	skipped := 0
	for i, op := range operations {
		// 停止先于下一语句生效，不能在取消后继续修改同一库。
		if err := ctx.Err(); err != nil {
			return "", err
		}
		source := op.SQL
		// 尚未执行的数据语句不能跟在未核验或已发现不一致的结构之后继续写入。
		if !op.Structural {
			if unverifiedDDL {
				return "", &VerificationError{Message: "前序结构未完成自动核验，后续数据语句未执行", Unverified: true}
			}
			if err := verify(ctx, access, finalChecks(appliedChecks)); err != nil {
				return "", err
			}
		}
		if op.Structural && op.Supported {
			// 已处理约束按作者顺序累计，数据写入前先检查这些目标是否已达到。
			for _, clause := range op.Clauses {
				appliedChecks = append(appliedChecks, clause.Checks...)
			}
			pending, skip, err := pendingStatement(ctx, access, op)
			if err != nil {
				return "", err
			}
			if skip {
				skipped++
				continue
			}
			source = pending
		}
		if op.Structural && !op.Supported {
			// 未知结构可以按原文尝试，但它之后的数据写入必须等待人工确认。
			unverifiedDDL = true
		}
		count, err := access.Exec(ctx, source)
		// 普通数据语句或服务端错误不因其他表已经存在而被吞掉。
		if err != nil {
			// 只处理已解析 DDL 的对象重复/缺失错误，数据唯一键冲突不能据此忽略。
			if op.Structural && op.Supported && isObjectStateError(err) {
				// 并发建立同名对象不是通过凭证，只有再次查询定义一致才允许继续。
				if checkErr := verify(ctx, access, finalChecks(appliedChecks)); checkErr != nil {
					return "", checkErr
				}
				skipped++
				continue
			}
			return "", fmt.Errorf("第 %d 条语句执行失败：%w", i+1, err)
		}
		affected += count
	}
	// 最后重新读取元数据，已有但类型不同的字段不能被计作成功跳过。
	if err := verify(ctx, access, checks); err != nil {
		return "", err
	}
	if len(unknownReasons) > 0 {
		return "", &VerificationError{Message: "SQL 已返回，部分结构语句仍需人工核验：" + strings.Join(unknownReasons, "；"), Unverified: true}
	}
	if len(checks) > 0 {
		return fmt.Sprintf("实际结构核验通过 · %d 条语句 · %d 条已存在或一致而跳过 · 影响 %d 行", len(statements), skipped, affected), nil
	}
	return fmt.Sprintf("%d 条数据语句完成 · 影响 %d 行", len(statements), affected), nil
}

// isObjectStateError 识别可通过元数据复核的对象状态竞争。
// 参数 err：执行 DDL 时的服务端错误，不接受普通数据约束冲突。
// 返回：仅表/字段/索引已存在或 DROP 目标已不存在时为 true。
func isObjectStateError(err error) bool {
	// 同名对象可能在检查与发送 DDL 之间被其他连接建立，仍须再次核验属性。
	var server *mysql.MySQLError
	if !errors.As(err, &server) {
		return false
	}
	// MySQL 标准错误码：表、字段、索引重复，以及 DROP 对象已不存在。
	const tableExists = 1050
	// 这些错误仅描述结构对象状态，不包含数据唯一键冲突 ER_DUP_ENTRY。
	const columnExists = 1060
	const indexExists = 1061
	const dropTargetMissing = 1091
	// 其他权限、语法或数据错误保留原失败语义，不能按“对象存在”吞掉。
	return server.Number == tableExists || server.Number == columnExists || server.Number == indexExists || server.Number == dropTargetMissing
}

// pendingStatement 去除已存在 ADD 对象或已经一致的 ALTER 子句。
// 参数 ctx：核验上下文；参数 access：同一单库适配器；参数 op：完整支持的 DDL 计划。
// 返回：仍需执行的语句、是否整条跳过及元数据错误。
func pendingStatement(ctx context.Context, access Access, op Operation) (string, bool, error) {
	// 元数据查询与实际执行使用同一个目标库，避免查错环境或查到其他租户。
	// ADD 的名称存在性只用于避免重复操作，属性差异留给最终核验。
	// MODIFY 必须在定义已经一致时才跳过，否则仍需实际修改。
	// 删除目标本来不存在时可省略该子句，不伪造新增结构。
	// 重组语句不会调整剩余子句的先后关系，也不会更换目标表。
	// 元数据读失败就不尝试写入，不能把权限不足误判成对象缺失。
	actual, err := access.Read(ctx, op.Table)
	if err != nil {
		return "", false, &VerificationError{Message: "执行前无法核验结构：" + err.Error(), Unverified: true}
	}
	if op.Create {
		// 旧表存在时先不执行 CREATE；定义差异由最终完整检查保留，后续 ALTER 仍能修正。
		return op.SQL, actual.Exists, nil
	}
	pending := []string{}
	changes := alterChanges{objects: map[string]bool{}}
	for _, clause := range op.Clauses {
		matches := true
		present := false
		for _, check := range clause.Checks {
			if Compare(check, actual) != "" {
				matches = false
			}
			if objectExists(check, actual) {
				present = true
			}
		}
		// ADD 已有对象只免除重复 DDL，绝不免除最终类型、默认值和索引定义核对。
		// 前序保留的子句若会改变本项依据，不能继续沿用执行前的存在性或位置。
		if !changes.affects(clause) && (matches || (clause.SkipExisting && present)) {
			continue
		}
		pending = append(pending, clause.SQL)
		// 只记录实际保留的动作，省略项不会改变数据库对象状态。
		changes.record(clause)
	}
	if len(pending) == 0 {
		return "", true, nil
	}
	// 仅对完整支持的普通 ALTER 重组，保留各子句顺序与安全引用的字符串语义。
	return "ALTER TABLE `" + strings.ReplaceAll(op.Table, "`", "``") + "` " + strings.Join(pending, ", "), false, nil
}

// verify 根据最终目标约束查询当前数据库结构。
// 参数 ctx：可取消上下文；参数 access：当前单库元数据入口；参数 checks：最后生效的约束。
// 返回：全部一致返回 nil；差异或查不到元数据时返回明确核验错误。
func verify(ctx context.Context, access Access, checks []Check) error {
	// 本轮快照按真实表名缓存，不跨库或跨任务复用。
	// 查询失败与表不存在是两种结果，不能统一当作空结构。
	// 同表的字段与索引来自这一次元数据读取，减少互相矛盾的报告。
	// 每个目标约束都必须比较，不能首项成功就提前通过整张表。
	// 返回的差异保留对象位置，方便定位到具体库中的字段或索引。
	// 一次核验中每张表只读一次，字段和索引来自同一轮元数据快照。
	tables := map[string]Table{}
	differences := []string{}
	for _, check := range checks {
		actual, exists := tables[check.Table]
		if !exists {
			var err error
			actual, err = access.Read(ctx, check.Table)
			if err != nil {
				return &VerificationError{Message: "无法完成结构核验：" + err.Error(), Unverified: true}
			}
			tables[check.Table] = actual
		}
		// 相同对象名称仍需检查完整目标属性，任何差异都阻止绿色成功状态。
		if check.Kind == "position" {
			// 不完整的顺序信息维持待核验，不能误记为已通过或普通业务失败。
			if message, complete := compareColumnPosition(check, actual); !complete {
				return &VerificationError{Message: message, Unverified: true}
			}
		}
		if detail := Compare(check, actual); detail != "" {
			differences = append(differences, detail)
		}
	}
	if len(differences) > 0 {
		return &VerificationError{Message: "结构不一致：" + strings.Join(differences, "；")}
	}
	return nil
}

// finalChecks 合并同一对象的连续定义，保留最后生效的结构。
// 参数 checks：按原始 SQL 顺序解析的全部约束。
// 返回：保持首次对象顺序、但使用最后定义的约束。
func finalChecks(checks []Check) []Check {
	// 同一对象可能在脚本中被多次修改，最后一次才定义本轮最终目标。
	// 对象类型参与键值，避免同名索引覆盖字段定义。
	// 新对象保留首次出现的位置，便于稳定展示与定位。
	// ADD 后 MODIFY 同字段常见于合并脚本，不能拿中间态要求最终数据库。
	result := []Check{}
	positions := map[string]int{}
	for _, check := range checks {
		// 位置约束需要按插入/移动顺序归并，不能简单按字段取最后一条。
		if check.Kind == "position" {
			continue
		}
		key := check.Table + "|" + check.Kind + "|" + strings.ToLower(check.Name)
		if index, ok := positions[key]; ok {
			result[index] = check
		} else {
			positions[key] = len(result)
			result = append(result, check)
		}
	}
	// 表本身和索引/列分别保留，不因同名字段覆盖表选项约束。
	return append(result, finalColumnPositions(checks)...)
}

// objectExists 仅判断 ADD 的同名目标是否已经存在。
// 参数 check：目标约束；参数 table：当前实时结构。
// 返回：名称存在为 true；完整属性相等由 Compare 和最终 verify 独立判断。
func objectExists(check Check, table Table) bool {
	// 字段存在与字段定义一致分别由两个检查负责。
	// 索引同名也可能列顺序不同，存在性不能作为最终通过条件。
	// 表存在性只支持重复 CREATE 的跳过决策。
	// 其他对象类型不自动猜测为已经存在。
	// 此结果只能避免重复 ADD，不能直接生成核验通过记录。
	switch check.Kind {
	case "column":
		_, ok := table.Columns[strings.ToLower(check.Name)]
		return ok
	case "index":
		_, ok := table.Indexes[strings.ToLower(check.Name)]
		return ok
	case "table":
		return table.Exists
	}
	return false
}
