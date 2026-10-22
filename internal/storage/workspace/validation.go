package workspace

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	sqlscript "release-desk/internal/sql/script"
)

// ValidateVersion 校验并规范化版本文件协议。
// 参数 version：待校验的完整版本文件指针；成功时在原对象补全合法缺省值。
// 返回：成功返回 nil 并补全可省略默认值；协议、名称或任何事项无效时返回错误。
func ValidateVersion(version *VersionFile) error {
	// 协议版本不接受隐式降级或猜测。
	// 文件名约束同时保护本地路径范围。
	// 版本状态必须属于软件支持的集合。
	// 每项稳定 ID 在同一版本内唯一。
	// 先校验全部记录再进入实际保存。

	// 固定协议标识与版本，不能把普通 JSON 或未知协议误当作发布记录。
	// 校验在写文件前完成，因此无效 AI 输入不会覆盖旧数据。

	if version.Format != "release-desk-version" || version.Schema != 1 {
		return errors.New("文件格式无效，请使用 AI 模板")
	}
	if !ValidName.MatchString(version.Version.Name) {
		return errors.New("版本名称需为 1–100 位字母、数字、中文或 .-_，不能包含路径")
	}

	if version.Version.Status == "" {
		version.Version.Status = "计划中"
	}
	if !slices.Contains([]string{"计划中", "进行中", "已发布"}, version.Version.Status) {
		return errors.New("版本状态无效")
	}

	// ID 稳定是 AI 增量更新的前提，重复 ID 会让不同事项错误覆盖彼此。
	// 允许没有任何事项的版本，用户可以先建版本再逐步记录。

	ids := map[string]bool{}
	for index := range version.Items {
		item := &version.Items[index]
		if item.ID == "" || ids[item.ID] {
			return errors.New("每项需有唯一且稳定的 id")
		}
		ids[item.ID] = true
		if err := NormalizeItem(item); err != nil {
			return err
		}
	}

	if version.Items == nil {
		version.Items = []Item{}
	}
	return nil
}

// NormalizeItem 校验单类事项并补全允许省略的状态。
// 参数 item：当前所选事项的独立快照，不接受模型修改执行或发布状态。
// 返回：成功返回 nil 并补全待办与展示状态；类型、名称、状态、配置要求或 SQL 元信息无效时返回错误。
func NormalizeItem(item *Item) error {
	// 名称使用本类型自己的首字段。
	// 不创建包含其他业务类型全部字段的大对象。
	// 状态缺省只代表待处理，不代表成功。
	// 业务变更类型与发布结果独立。

	// 复用与界面、模板相同的字段元数据，第一项是该类型的业务名称。
	// 空名称不能用于发布核对，也不能形成稳定可识别的记录。
	definition, ok := Fields[item.Kind]
	if !ok {
		return errors.New("记录类型无效")
	}

	if strings.TrimSpace(item.Data[definition[0][0]]) == "" {
		return fmt.Errorf("%s不能为空", definition[0][1])
	}

	// 待办进度与实际上线、SQL 执行结果分别维护，不能互相推导。
	// 省略状态表示新增待办，而不是默认已经完成。
	if item.Status == "" {
		item.Status = "待处理"
	}
	if !slices.Contains([]string{"待处理", "进行中", "已完成"}, item.Status) {
		return errors.New("待办状态无效")
	}

	if item.Action == "" {
		item.Action = "新增"
	}
	// 定时任务可关闭并保留定义，其他类型不继承这一特有操作。
	if !slices.Contains(ItemActionOptions(item.Kind), item.Action) {
		return errors.New("该记录类型不支持此操作")
	}
	// Cron 只存表达式；旧格式的自然语言说明移入描述，手动与 AI 文件使用同一协议。
	if item.Kind == "job" {
		NormalizeJobFrequency(item.Data)
	}

	// SQL 入库只校验必填信息，语法与目标范围在执行预览时检查。
	// 这样扩展语法脚本仍能被记录、查看和复制，暂不支持执行时给出明确提示。
	if item.Kind == "sql" {
		if err := ValidateSQLMetadata(item.Data); err != nil {
			return err
		}
	}
	// 配置要求独立于上线状态；旧文件未声明时保留待确认，不能默认为可选。
	// 无法识别的值应在写文件前拒绝，避免 AI 错误输出覆盖用户记录。
	if item.Kind == "config" {
		if err := ValidateConfigRequirement(item.Data); err != nil {
			return err
		}
	}

	if item.Online == "" {
		item.Online = "待上线"
	}
	if !slices.Contains([]string{"待上线", "已上线"}, item.Online) {
		return errors.New("线上发布状态无效")
	}
	if item.VerifyState == "" {
		item.VerifyState = "待验证"
	}
	return nil
}

// ValidateSQLMetadata 校验 SQL 所属端、内容与展示顺序。
// 参数 data：本方法的业务数据；格式与取值由本方法的校验入口约束。
// 返回：成功返回 nil 并补全默认顺序；SQL 为空、端无效或顺序格式错误时返回错误。
func ValidateSQLMetadata(data map[string]string) error {
	// 所属端必须明确，不能猜测跨租户目标。

	// 不能让未知端自动落到租户端，避免一条脚本扩散到错误的数据库集合。
	// 模板库属于相应端的一部分，具体库名由匹配规则统一决定。
	if strings.TrimSpace(data["sql"]) == "" {
		return errors.New("SQL 内容不能为空")
	}
	if !slices.Contains([]string{"租户端", "服务商端", "平台端"}, data["side"]) {
		return errors.New("SQL 必须指定所属端")
	}
	// 每次保存都从语句重新识别，忽略文件路径和外部手工标签，内容更新后类型不会过期。
	data["phase"] = sqlscript.InferSQLPhase(data)
	if !slices.Contains(sqlscript.SQLPhases, data["phase"]) {
		return errors.New("SQL 执行阶段无效")
	}

	// 顺序只用于排序与提醒；点击执行仍针对当前选中的单条记录。
	// 不自动串行运行其他 SQL，防止用户预览一项却触发整个版本。
	if data["order"] == "" {
		data["order"] = "1"
	}
	if !regexp.MustCompile(`^\d{1,6}$`).MatchString(data["order"]) {
		return errors.New("执行顺序应为 0–999999 的整数")
	}
	return nil
}
