package fileservice

import (
	"bytes"
	"encoding/json"
	"html/template"
	"os"
	"path/filepath"
	"time"

	"release-desk/internal/platform/desktop"
	"release-desk/internal/storage/fileio"
	"release-desk/internal/storage/workspace"
)

const ReportHTML = `<!doctype html><html lang="zh-CN"><meta charset="utf-8"><title>{{.Version.Name}} 发布清单</title>
<style>body{font:14px/1.8 "Microsoft YaHei",sans-serif;max-width:1000px;margin:50px auto;padding:0 30px;color:#273249;background:#f4f6fb}h1{font-size:34px}h2{margin-top:38px}article{background:white;border:1px solid #e1e6f0;border-radius:12px;padding:20px 25px;margin:16px 0;break-inside:avoid}pre{white-space:pre-wrap;overflow-wrap:anywhere;background:#f6f7fb;border-radius:6px;padding:14px;font:13px/1.8 Consolas,"Microsoft YaHei",monospace}dt{font-weight:bold;color:#78849b}dd{margin:0 0 16px}small{color:#8a95ab}@media print{body{background:white;margin:0}}</style>
<small>RELEASE DESK / 版本发布清单</small><h1>{{.Version.Name}}</h1><p>{{.Version.Status}} · {{.Version.Date}}</p><pre>{{.Version.Notes}}</pre>
{{range .Sections}}<h2>{{.Title}} · {{len .Rows}}</h2>{{range .Rows}}<article><h3>{{.Title}}</h3><p>{{.Action}} · {{.Status}} · {{.Service}} · {{.Online}}</p><dl>{{range .Fields}}<dt>{{.Label}}</dt><dd><pre>{{.Value}}</pre></dd>{{end}}</dl></article>{{else}}<p>本版本暂无此类记录</p>{{end}}{{end}}<small>导出于 {{.At}}；SQL 执行审计请在应用内查看或导出完整备份。</small></html>`

type ReportField struct{ Label, Value string }

type ReportRow struct {
	Title, Action, Status, Service, Online string
	Fields                                 []ReportField
}

type ReportSection struct {
	Title string
	Rows  []ReportRow
}

// RenderReport 生成已转义的版本发布清单。
// 参数 v：当前选中版本的独立完整快照，包含其五类记录。
// 返回：返回 HTML 清单字节；模板生成失败时返回错误，记录内容均作为转义文本。
func RenderReport(v workspace.VersionFile) ([]byte, error) {
	// 报告按软件固定业务顺序展示。
	// 版本记录使用独立快照，不受界面继续编辑影响。
	// 只有当前类型的字段进入该类型报告。
	// 配置与 SQL 作为文本，不当作 HTML 执行。
	// 字段名称来自统一元数据。
	// 报告保留待办与线上标记的区别。
	// 导出时间只说明生成时刻，不是发布时间。

	sections := []ReportSection{}
	// html/template 转义配置和 SQL，导出文件不会执行记录中的 HTML。

	for _, kind := range workspace.Kinds {
		section := ReportSection{Title: workspace.KindLabels[kind]}
		for _, item := range v.Items {
			if item.Kind != kind {
				continue
			}
			row := ReportRow{Title: item.Data[workspace.Fields[kind][0][0]], Action: item.Action, Status: item.Status, Service: item.Service, Online: item.Online}
			for _, field := range workspace.Fields[kind] {
				row.Fields = append(row.Fields, ReportField{Label: field[1], Value: item.Data[field[0]]})
			}
			section.Rows = append(section.Rows, row)
		}
		sections = append(sections, section)
	}

	compiled, err := template.New("report").Parse(ReportHTML)

	if err != nil {
		return nil, err
	}

	var output bytes.Buffer

	err = compiled.Execute(&output, map[string]any{"Version": v.Version, "Sections": sections, "At": workspace.Stamp()})
	return output.Bytes(), err
}

// Export 将选中版本清单导出到本机。
// 参数 body：界面传入的 UTF-8 JSON 请求；已通过本机能力令牌鉴权，仍需进行业务字段校验。
// 返回：返回 HTML 清单绝对路径；版本不存在或导出失败时返回错误。
func (a *Service) Export(body []byte) (any, error) {
	// 只允许导出已经存在的版本。
	// 获得副本后释放记录锁再生成 HTML。
	// 报告文件放入应用数据导出目录。
	// 版本名已经过文件名安全校验。
	// 时间戳避免覆盖先前导出文件。
	// 原子写入避免导出半个文件。
	// 成功后打开目录，便于用户复制或发送。

	var req workspace.Version

	if err := json.Unmarshal(body, &req); err != nil {
		return nil, err
	}
	a.Store.Mu.Lock()

	version, ok := a.Store.Versions[req.Name]
	version = workspace.Clone(version)

	a.Store.Mu.Unlock()
	// 导出读取快照，避免记录更新影响生成到一半的清单。
	if !ok {
		return nil, os.ErrNotExist
	}

	content, err := RenderReport(version)
	if err != nil {
		return nil, err
	}

	folder := filepath.Join(a.Store.Dir, "导出")
	path := filepath.Join(folder, req.Name+"-发布清单-"+time.Now().Format("20060102-150405")+".html")

	if err = fileio.AtomicWrite(path, content); err != nil {
		return nil, err
	}
	desktop.OpenPath(folder)
	return path, nil
}
