package api

import (
	"bytes"
	_ "embed"
	"html/template"
)

//go:embed assets/openapi.yaml
var openAPI []byte

// Offline API documentation: no CDN scripts, external fonts, or file-system dependency.
var docsPage = func() []byte {
	t := template.Must(template.New("docs").Parse(`<!doctype html>
<html lang="zh-CN"><meta charset="utf-8"><meta name="viewport" content="width=device-width">
<title>OA Agent API 文档</title>
<style>body{max-width:960px;margin:40px auto;padding:0 24px;font:16px/1.6 system-ui;color:#202124}pre{padding:20px;background:#f4f6f8;overflow:auto}a{color:#1756a9}</style>
<h1>OA Agent API · M0</h1>
<p>当前仅实现健康检查。导入、识别、审批及归档接口尚未实现，业务契约未冻结。</p>
<ul><li><a href="/healthz">GET /healthz</a>：服务存活。</li>
<li><a href="/readyz">GET /readyz</a>：数据库与 Worker 状态；两项都正常返回 200，否则 503。</li>
<li><a href="/openapi.yaml">下载 OpenAPI 3.1 契约</a>，可导入兼容的 API 工具。</li></ul>
<p>就绪表示基础依赖正常，当前 Worker 尚不支持文档提取。以下为当前程序编译时嵌入的契约。</p>
<h2>OpenAPI</h2><pre>{{.}}</pre></html>`))
	var output bytes.Buffer
	if err := t.Execute(&output, string(openAPI)); err != nil {
		panic(err)
	}
	return output.Bytes()
}()
