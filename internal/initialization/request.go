package initialization

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"release-desk/internal/storage/workspace"
)

// buildRequest 将用户确认的环境、已保存路由及临时参数组装为单次 HTTP 请求。
// 参数 ctx：本次取消上下文；参数 item：已定位的初始化事项快照。
// 参数 input：本次环境、参数和临时 Token；参数 config：固定的环境入口配置。
// 返回：待发送请求及用于屏蔽回显的 Token；校验失败时不联网。
func buildRequest(ctx context.Context, item workspace.Item, input Request, config workspace.Settings) (*http.Request, string, error) {
	// Token 必须每次提供，既不从事项读取，也不写回设置。
	secret := strings.TrimSpace(input.Token)
	// 用户可粘贴裸 Token 或完整 Bearer 值，发送时只保留一个固定方案前缀。
	if strings.HasPrefix(strings.ToLower(secret), "bearer ") {
		secret = strings.TrimSpace(secret[7:])
	}
	// 换行不能进入请求头；拒绝而非清洗，避免把无效凭据误发出去。
	if secret == "" || strings.ContainsAny(secret, "\r\n") {
		return nil, "", errors.New("请填写本次有效 Token")
	}
	// 环境和端对应用户已经确认的入口，旧设置缺失时补充同一份集中默认值。
	baseURLs := config.InitializationBaseURLs
	if baseURLs == nil {
		baseURLs = workspace.DefaultInitializationBaseURLs()
	}
	// 未配置的环境直接报错，线上调用不会回退到测试入口。
	base := strings.TrimRight(baseURLs[input.Environment][item.Data["side"]], "/")
	// 环境入口也须与弹窗一致，配置更新不能让临时 Token 发往另一个地址。
	if input.BaseURL != "" && strings.TrimRight(input.BaseURL, "/") != base {
		return nil, "", errors.New("环境入口已变更，请重新打开手动调用")
	}
	endpoint := strings.TrimSpace(item.Data["url"])
	// 服务内路径不包含网关服务前缀；不能在缺失调用地址时回退到根地址或此路径。
	if endpoint == "" {
		return nil, "", errors.New("调用路径待确认，请先补全含服务前缀的网关调用路径")
	}
	// 只从配置的三端入口发送；版本文件只记录相对接口路径，不接受另一绝对主机。
	if base == "" || !strings.HasPrefix(endpoint, "/") || strings.HasPrefix(endpoint, "//") || strings.Contains(endpoint, "://") {
		return nil, "", errors.New("请选择环境、所属端并填写以 / 开头的接口路径")
	}
	// 避免服务商路径已经包含 /spapi 时重复拼接前缀。
	baseURL, err := url.Parse(base)
	// base 仅包含协议、主机和路由前缀，凭据或查询参数不能藏在域名配置中。
	if err != nil || baseURL.Host == "" || baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" || (baseURL.Scheme != "https" && baseURL.Scheme != "http") {
		return nil, "", errors.New("初始化接口环境地址无效")
	}
	// 有些来源已经给出网关前缀，只在完整路径边界匹配时去重。
	if baseURL.Path != "" && (endpoint == baseURL.Path || strings.HasPrefix(endpoint, baseURL.Path+"/") || strings.HasPrefix(endpoint, baseURL.Path+"?")) {
		base = strings.TrimSuffix(base, baseURL.Path)
	}
	// 最终地址从唯一入口与相对路径组合，浏览器中的文字预览不能改变发送目标。
	target, err := url.Parse(base + endpoint)
	if err != nil || target.Fragment != "" {
		return nil, "", errors.New("接口路径无效")
	}
	// 参数为用户本次填写值；无参接口不允许悄悄使用残留模板。
	query, payload, err := requestParameters(item, input)
	// 必填检查在联网之前完成，不用服务端错误代替参数完整性提示。
	if err != nil {
		return nil, "", err
	}
	// Query 独立编码，参数值中的 &、中文和空格不会成为新的路由片段。
	values := target.Query()
	for key, value := range query {
		values.Set(key, value)
	}
	target.RawQuery = values.Encode()
	// 请求方法取已保存记录，不能由临时调用弹窗偷偷切换写入方式。
	method := strings.ToUpper(strings.TrimSpace(item.Data["method"]))
	if method == "" {
		return nil, "", errors.New("请先填写接口请求方法")
	}
	// Body 保留用户 JSON 原文，尤其不能经过浮点编码改变长业务 ID。
	req, err := http.NewRequestWithContext(ctx, method, target.String(), strings.NewReader(payload))
	if err != nil {
		return nil, "", errors.New("接口方法或参数无法组成请求")
	}
	// 鉴权只存在本次请求头；响应与业务状态由用户结合接口语义确认。
	req.Header.Set("Authorization", "Bearer "+secret)
	req.Header.Set("Accept", "application/json")
	// 无参调用没有 Body，不发送虚构的空 JSON 或额外 Content-Type。
	if payload != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, secret, nil
}

// requestParameters 校验无参与必填参数，保留 JSON 中零值、布尔值及数值原文。
// 参数 item：参数分类与模板所属事项；参数 input：用户本次填写的 Query 与 Body 文本。
// 返回：Query 键值与原始 JSON Body；缺失或空白参数拒绝发送。
func requestParameters(item workspace.Item, input Request) (map[string]string, string, error) {
	// 明确无参时不复用输入框或记录中的残留参数。
	if item.Data["parameter_mode"] == "无参" {
		return map[string]string{}, "", nil
	}
	if item.Data["parameter_mode"] != "必填参数" {
		// 未确认的分类不能退化成无参调用，避免缺失业务范围参数。
		return nil, "", errors.New("请先确认接口参数要求")
	}
	// 模板列出的字段都必须出现，不能删掉必填项后用其他参数绕过完整性检查。
	for _, pair := range [][2]string{{item.Data["params"], input.Params}, {item.Data["body"], input.Body}} {
		// Query 与 Body 可只用一种；没有模板的一侧不强制用户凭空填写。
		if strings.TrimSpace(pair[0]) == "" {
			continue
		}
		// 模板只约束字段结构，不能把占位文字当成真实参数默认值。
		var template, actual any
		if json.Unmarshal([]byte(pair[0]), &template) != nil {
			return nil, "", errors.New("已保存的参数模板不是有效 JSON，请先编辑事项")
		}
		// 已列出的字段必须全部存在，不能通过删除字段绕开必填检查。
		if json.Unmarshal([]byte(pair[1]), &actual) != nil || missingTemplateField(template, actual) {
			return nil, "", errors.New("请完整填写模板列出的必填参数")
		}
	}
	// 发往 Query 的值单独收集，不将整个 JSON 对象序列化成一个参数。
	values := map[string]string{}
	var query map[string]json.RawMessage
	if strings.TrimSpace(input.Params) != "" {
		// Query 要求对象格式，原始数字保持 RawMessage 以避免精度变化。
		if err := json.Unmarshal([]byte(input.Params), &query); err != nil {
			return nil, "", errors.New("Query 参数应为 JSON 对象")
		}
	}
	for key, raw := range query {
		// Query 仅接受标量；数字原文保留，避免长 ID 经浮点数转换后失真。
		value := strings.TrimSpace(string(raw))
		isNull := value == "null"
		isContainer := strings.HasPrefix(value, "{") || strings.HasPrefix(value, "[")
		// JSON null 表示未填，字符串 "null" 则是用户明确给出的非空业务值。
		if strings.HasPrefix(value, "\"") {
			// 只解码字符串的转义符，数值与布尔原文直接用于 URL 编码。
			if err := json.Unmarshal(raw, &value); err != nil {
				return nil, "", errors.New("Query 参数格式无效")
			}
		}
		// 当前参数表约定 Query 为标量，嵌套业务结构请放到 Body 模板。
		if strings.TrimSpace(key) == "" || strings.TrimSpace(value) == "" || isNull || isContainer {
			return nil, "", errors.New("Query 必填参数不能为空，且应为字符串、数字或布尔值")
		}
		values[key] = value
	}
	// Body 只做合法性与完整性检查，不重排字段、不改写数字字符串。
	payload := strings.TrimSpace(input.Body)
	if payload != "" {
		var body any
		// JSON 语法正确也不代表必填值完整，空容器和嵌套空值仍需补充。
		if json.Unmarshal([]byte(payload), &body) != nil || emptyParameter(body) {
			return nil, "", errors.New("Body 必填参数需为有效 JSON，模板中的参数值不能为空")
		}
	}
	// 有参接口不能以两个空输入框发送；零和 false 是合法业务值。
	if len(values) == 0 && payload == "" {
		return nil, "", errors.New("请填写本次必填参数")
	}
	return values, payload, nil
}

// missingTemplateField 检查模板定义的对象字段是否仍存在，允许本次输入额外字段。
// 参数 template：已保存 JSON 模板；参数 actual：用户本次 JSON 输入，不包含鉴权 Token。
// 返回：对象字段或数组元素结构缺失时为 true，不比较业务值与占位符文字。
func missingTemplateField(template, actual any) bool {
	// 这里只比较字段结构，空白值由独立完整性检查处理。
	switch expected := template.(type) {
	case map[string]any:
		// 模板要求对象时，标量输入不能替代整组字段。
		values, ok := actual.(map[string]any)
		if !ok {
			// 结构不匹配属于填写错误，不尝试猜测字段值。
			return true
		}
		for key, child := range expected {
			// 对嵌套对象同样检查，避免只填写顶层而丢失内部必填字段。
			value, exists := values[key]
			if !exists || missingTemplateField(child, value) {
				return true
			}
		}
	case []any:
		// 必填数组必须至少给出一项，空数组不能绕过元素模板。
		values, ok := actual.([]any)
		if !ok || len(values) == 0 {
			// 数组长度可变化，但不能缺失整组业务输入。
			return true
		}
		// 数组模板首项代表元素结构，不要求实际业务项数等于示例数量。
		if len(expected) > 0 {
			for _, value := range values {
				if missingTemplateField(expected[0], value) {
					return true
				}
			}
		}
	}
	// 标量类型的业务取值不与模板占位符做相等判断。
	return false
}

// emptyParameter 判断必填模板是否仍有空值，不将零或 false 当作缺失。
// 参数 value：仅供校验的已解码 JSON 值；不用于重写实际发送的数值。
// 返回：空字符串、null、空容器或内部未填值时为 true。
func emptyParameter(value any) bool {
	// 递归仅用于完整性检查，实际请求仍使用用户 JSON 原文。
	switch typed := value.(type) {
	case nil:
		// null 不能替代必填值；需要可空语义时应先调整接口记录的参数约定。
		return true
	case string:
		// 空白占位符仍需人工填写，而非直接提交服务端。
		return strings.TrimSpace(typed) == ""
	case map[string]any:
		// 必填对象不能只保留空壳，内部字段也按相同规则检查。
		if len(typed) == 0 {
			return true
		}
		for _, part := range typed {
			// 遇到任一未填字段就停止，不为展示拼装包含参数值的错误正文。
			if emptyParameter(part) {
				return true
			}
		}
	case []any:
		// 必填集合需要实际业务项，不能将空数组误认为已填写。
		if len(typed) == 0 {
			return true
		}
		for _, part := range typed {
			// 集合中的每一项均需完整，避免只校验第一项后漏掉其余占位值。
			if emptyParameter(part) {
				return true
			}
		}
	}
	// 数字和布尔值不受字符串空白规则影响。
	return false
}
