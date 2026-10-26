# OCR 抢救技能（ocr_rescue）设计

目标：OCR 质量差（倾斜/旋转/低对比/小字/噪点/拍照）时，不靠运气，而是让 Agent 按“技能”走一套有据可依的补救流程，并对最终结果诚实标注。

## 1. 技能机制（harness）
- `Skill{Name, Summary, Instructions string, Tools []string}`。系统提示只列技能目录（名称+一句话），模型需要时调用工具 `load_skill{name}` 取得详细步骤（渐进披露），同时**解锁**该技能的工具；未解锁的工具不出现在发给模型的工具列表里。
- 本期技能：`ocr_rescue`。解锁工具：`assess_ocr`、`rerun_ocr`、`read_page_image`（仅当视觉可用）。
- 离线 Heuristic provider 也走同一机制：在 `run_ocr` 之后自动 `load_skill ocr_rescue` → `assess_ocr`，判定 poor 时按建议顺序最多 `rerun_ocr` 2 次，再继续原流程。
- 工具仍是只读、无路径参数、有 Schema/超时；文档内容仍用 DOCUMENT_DATA 包裹。

## 2. Worker 接口扩展（回环 + Bearer，与现有 /v1/extract 并存）
`POST /v1/extract` 请求新增可选字段：
- `ocr_profile`：`default`（默认，行为与现在完全一致）｜`enhanced`（放大到长边≥2000px + CLAHE 对比度 + 轻度去噪）｜`rotate`（在缩小图上尝试 0/90/180/270，取置信度最高的方向后再整页识别）｜`deskew`（估计倾斜角并校正，|角度|≤15°）｜`high_dpi`（PDF 以 300 dpi 渲染；图片按 2x 放大）｜`binarize`（自适应二值化）。未知值 → 400 INVALID_REQUEST。
- `pages`：可选，1 起始的页码数组（≤50 项）；只对这些页做 OCR（其余页保持原生文本/空）。

响应 `data` 新增 `quality`（未用 OCR 时为 `null`）：
```json
{"mean_confidence":0.87,"low_confidence_ratio":0.12,"chars":420,"cjk_ratio":0.8,"noise_ratio":0.03,"lines":31,
 "profile":"enhanced","rotation":90,"weak_pages":[2],
 "low_confidence_lines":[{"page":1,"text":"…≤80 字…","confidence":0.41}]}
```
`low_confidence_lines` 最多 10 条；`mean_confidence` 无法计算时为 null；`rotation` 为实际采用的旋转角度（未旋转为 0）。

新增 `POST /v1/render`（供视觉模型读取页面图）：请求 `{"path","page":1,"max_side":1400}`（max_side 256–2000）；响应 `{"data":{"image_base64","mime_type":"image/png","width","height","page","pages"}}`；图片编码后 ≤2.5 MB（超出则自动缩小）。路径限制、大小限制、错误码与 /v1/extract 相同；`page` 越界 → 400 INVALID_REQUEST。不能渲染（Office 文档、文本等非 PDF/图片）→ 422 UNSUPPORTED_FORMAT。图片格式含 png/jpg/jfif/bmp/webp/gif/tif(f)；多页 TIFF 的 `page` 为帧序号（最多 500 页），GIF/WebP 只有第 1 页。

## 3. Go 侧约定
- `ExtractRequest{OCRProfile string; Pages []int}`；`ExtractResult.Quality *OCRQuality`（字段同上，Go 命名驼峰、JSON 同上）。
- `PageRenderer` 接口：`Render(ctx, RenderRequest{Path string; Page, MaxSide int}) (RenderResult{ImageBase64, MIMEType string; Width, Height, Page, Pages int}, error)`，由 `WorkerExtractor` 实现。
- 视觉：`VisionTranscriber{ Transcribe(ctx, image []byte, mime, instruction string) (string, error) }`；`Options.Vision func() VisionTranscriber`（func 为 nil 或返回 nil 表示未启用），每次使用时动态调用（设置可热更新）。`read_page_image` 只在 `Vision() != nil` 且渲染器可用时解锁；它**分别**调用视觉模型转写页面，返回的文字同样当作不可信数据。
- 判定 `assess_ocr`（确定性，Go 实现）：综合 `mean_confidence`、`low_confidence_ratio`、`noise_ratio`、`chars`、关键字段是否在文本中找得到（日期/部门/类型的证据）；输出 `verdict: good|fair|poor`、`score(0–100)`、`reasons[]`、`suggested_profiles[]`（按最可能见效排序：怀疑旋转→rotate；置信度低且字小→enhanced/high_dpi；倾斜→deskew；对比度差/噪点→binarize/enhanced）。
- `rerun_ocr{profile,pages?}`：保留**得分最高**的 OCR 结果为会话文本（不是“最长”），并返回新旧得分对比；最多 4 次/文件。
- 容错：日期/数字抽取容忍 OCR 常见混淆（`O/o→0`、`l/I/|→1`、全角数字、`2O26年3月l5日`），仅在数字语境下生效，证据中注明“OCR 纠错”。
- 质量→审核：最终文本主要来自 OCR 且 verdict 为 poor（含重试后）→ `ReviewRequired=true` 并给出警告 `ocr_quality:poor`；`Result` 增加 `OCR *OCRSummary{Used bool; Quality string; MeanConfidence *float64; Profile string; Attempts int; VisionUsed bool}`。
- 事件（脱敏）：`skill_loaded`、`ocr_assessed`（verdict/score）、`ocr_retry`（profile、前后得分）、`vision_used`；不含正文。

---

# 文档预览（preview）设计

目标：在应用内直接预览原件——PDF/图片按页显示，Word/Excel 解析成结构化内容再渲染（前端不使用 v-html，天然无 XSS）。

## Worker：新增 `POST /v1/preview`
请求 `{"path":"…绝对路径…","max_rows":200,"max_blocks":600}`，路径必须位于**允许的根目录**内（`OA_SPOOL_ROOT` 以及可选的 `OA_ALLOWED_ROOTS`，多个以系统路径分隔符分隔——归档根目录需加入其中才能预览已归档文件）。响应 `data`：
- docx：`{"kind":"document","blocks":[…],"truncated":bool,"warnings":[]}`。`blocks[]` 元素：`{"type":"heading","level":1-6,"text":"…"}`｜`{"type":"paragraph","runs":[{"text":"…","bold":true,"italic":false}],"align":"left|center|right"}`｜`{"type":"list_item","level":0,"runs":[…]}`｜`{"type":"table","rows":[["单元格文本",…],…]}`。含页眉/页脚文字放在 `warnings` 之外的 `header`/`footer` 字符串字段（可空）。图片不输出，只在 `warnings` 记“含 N 张图片未显示”。
- xlsx：`{"kind":"spreadsheet","sheets":[{"name":"Sheet1","rows":[["A1","B1"],…],"rows_total":1234,"cols_total":8,"truncated":true}],"truncated":bool,"warnings":[]}`（每表最多 `max_rows` 行、50 列，单元格文本 ≤200 字符，公式显示计算值缓存，缺失则显示公式文本；隐藏表不输出）。
- pdf：`{"kind":"pdf","pages":N}`；图片（含 bmp/webp/gif/tif）：`{"kind":"image","pages":N}`（多页 TIFF 为页数）；Word/PPT/文本/网页/邮件（含 97-2003、ODF、RTF）→ `document`，Excel/CSV/ODS（含 .xls）→ `spreadsheet`，与 docx/xlsx 同一套结构化渲染（行/列/块数有上限）；格式按**内容**识别，不信任扩展名，扩展名与内容不符时 `warnings` 带说明；无法识别的二进制 → 422 UNSUPPORTED_FORMAT；加密 → 422 ENCRYPTED；损坏 → 422 UNREADABLE_FILE。
页面图片由 `POST /v1/render`（与 OCR 抢救共用）提供。

## API（Go）
- `GET /v1/files/{id}/preview` — 导入时保存的原件副本；`GET /v1/files/{id}/preview/page?page=1&max_side=1400` → `image/png`。
- `GET /v1/fs/preview?path=` — 归档目录中的文件；`GET /v1/fs/preview/page?path=&page=&max_side=` → `image/png`。
- 预览 JSON：`{"kind","name","mime_type","size_bytes","pages":int|null,"blocks"?,"header"?,"footer"?,"sheets"?,"truncated","warnings":[],"page_url":string|null}`；`page_url` 为带 `{page}` 占位符的相对 URL。`kind=unsupported` 时 `message` 说明原因（格式不识别，可下载查看；文件仍可按文件名归档）。
- 安全：只读；路径经 fsman/记录 ID 解析，永不接受绝对路径；页面图片 `Cache-Control: no-store`、`nosniff`。
