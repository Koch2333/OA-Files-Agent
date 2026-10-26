# Generates GENUINE Microsoft Office-produced fixtures (Word/Excel/PowerPoint 97-2003 binary, ODF, RTF, HTML) through
# COM automation. Requires Windows with Office installed; output directory is the first argument.
# Usage: powershell -ExecutionPolicy Bypass -File make_office_fixtures.ps1 -Out D:\path\to\tests\samples\formats
param([Parameter(Mandatory = $true)][string]$Out)
$ErrorActionPreference = 'Stop'
New-Item -ItemType Directory -Force -Path $Out | Out-Null
$miss = [System.Reflection.Missing]::Value

function New-WordDoc($w) {
    $d = $w.Documents.Add()
    $sel = $w.Selection
    $sel.Style = $d.Styles.Item(-2)  # Heading 1
    $sel.TypeText("费用报销申请单"); $sel.TypeParagraph()
    $sel.Style = $d.Styles.Item(-1)  # Normal
    $sel.Font.Bold = 1; $sel.TypeText("申请部门："); $sel.Font.Bold = 0
    $sel.TypeText("财务部    Date: 2026-03-15    Amount 3280.00"); $sel.TypeParagraph()
    $sel.TypeText("Mixed English and 中文 text, 全角标点：（测试）。 Café naïve."); $sel.TypeParagraph()
    $tbl = $d.Tables.Add($sel.Range, 3, 3)
    $tbl.Cell(1, 1).Range.Text = "项目"; $tbl.Cell(1, 2).Range.Text = "数量"; $tbl.Cell(1, 3).Range.Text = "金额"
    $tbl.Cell(2, 1).Range.Text = "差旅费"; $tbl.Cell(2, 2).Range.Text = "2"; $tbl.Cell(2, 3).Range.Text = "1280.00"
    $tbl.Cell(3, 1).Range.Text = "办公用品"; $tbl.Cell(3, 2).Range.Text = "10"; $tbl.Cell(3, 3).Range.Text = "2000.00"
    $sel.EndKey(6) | Out-Null  # wdStory
    $sel.TypeParagraph()
    $sel.Range.ListFormat.ApplyBulletDefault()
    $sel.TypeText("第一项审批意见"); $sel.TypeParagraph(); $sel.TypeText("第二项审批意见")
    $sel.Range.ListFormat.RemoveNumbers()
    $sel.TypeParagraph(); $sel.TypeText("末尾段落 END-OF-DOC")
    $sec = $d.Sections.Item(1)
    $sec.Headers.Item(1).Range.Text = "某某集团有限公司 内部文件"
    $sec.Footers.Item(1).Range.Text = "页脚 Footer-Marker"
    return $d
}

$w = New-Object -ComObject Word.Application
$w.Visible = $false; $w.DisplayAlerts = 0
try {
    $d = New-WordDoc $w
    $d.SaveAs2([ref](Join-Path $Out 'word97_report.doc'), [ref]0)       # wdFormatDocument97
    $d.SaveAs2([ref](Join-Path $Out 'word_report.rtf'), [ref]6)         # wdFormatRTF
    $d.SaveAs2([ref](Join-Path $Out 'word_report.odt'), [ref]23)        # wdFormatOpenDocumentText
    $d.SaveAs2([ref](Join-Path $Out 'word_report.html'), [ref]10)       # wdFormatFilteredHTML
    $d.Close([ref]0)
    # Latin-only document (all pieces are compressed 8-bit "cp1252" text)
    $d = $w.Documents.Add(); $d.Content.Text = "Plain ASCII document. Quarterly report Q1 2026.`rSecond paragraph with caf" + [char]0xE9 + "."
    $d.SaveAs2([ref](Join-Path $Out 'word97_latin.doc'), [ref]0); $d.Close([ref]0)
    # Password protected (open password)
    $d = $w.Documents.Add(); $d.Content.Text = "secret contents"
    $d.Password = 'oa-test-pass'
    $d.SaveAs2([ref](Join-Path $Out 'word97_encrypted.doc'), [ref]0)
    $d.Close([ref]0)
    # Fast-saved style: edit twice so the piece table has several pieces
    $d = $w.Documents.Open((Join-Path $Out 'word97_latin.doc'))
    $d.Content.InsertAfter("`rAppended later 追加内容"); $d.Content.InsertBefore("Inserted first. ")
    $d.SaveAs2([ref](Join-Path $Out 'word97_edited.doc'), [ref]0); $d.Close([ref]0)
} finally { $w.Quit() }

$x = New-Object -ComObject Excel.Application
$x.Visible = $false; $x.DisplayAlerts = $false
try {
    $wb = $x.Workbooks.Add()
    while ($wb.Worksheets.Count -lt 3) { $wb.Worksheets.Add() | Out-Null }
    $s1 = $wb.Worksheets.Item(1); $s1.Name = "采购明细"
    $s1.Range("A1").Value2 = "序号"; $s1.Range("B1").Value2 = "名称"; $s1.Range("C1").Value2 = "数量"; $s1.Range("D1").Value2 = "单价"; $s1.Range("E1").Value2 = "金额"; $s1.Range("F1").Value2 = "日期"; $s1.Range("G1").Value2 = "含税"
    for ($i = 2; $i -le 6; $i++) {
        $s1.Range("A$i").Value2 = [double]($i - 1)
        $s1.Range("B$i").Value2 = [string]("设备" + ($i - 1))
        $s1.Range("C$i").Value2 = [double]($i * 2)
        $s1.Range("D$i").Value2 = [double](100.5 * $i)
        $s1.Range("E$i").Formula = "=C$i*D$i"
        $s1.Range("F$i").Formula = "=DATE(2026,3,$i)"
        $s1.Range("G$i").Value2 = [bool]($i % 2)
    }
    $s1.Range("F2:F6").NumberFormat = "yyyy-mm-dd"
    $s1.Range("A8").Value2 = "汇总"; $s1.Range("E8").Formula = "=SUM(E2:E6)"
    $s1.Range("A10:C10").Merge(); $s1.Range("A10").Value2 = "合并单元格 merged"
    $s2 = $wb.Worksheets.Item(2); $s2.Name = "Sheet 2"; $s2.Range("A1").Value2 = "second sheet"; $s2.Range("B2").Value2 = "中文"
    $s2.Range("A3").Formula = "=1/0"   # error cell #DIV/0!
    $s3 = $wb.Worksheets.Item(3); $s3.Name = "隐藏表"; $s3.Range("A1").Value2 = "hidden sheet content"; $s3.Visible = 0
    $wb.SaveAs((Join-Path $Out 'excel97_purchase.xls'), 56)             # xlExcel8
    $wb.SaveAs((Join-Path $Out 'excel_purchase.ods'), 60)               # xlOpenDocumentSpreadsheet
    $wb.Close($false)
    $wb = $x.Workbooks.Add(); $wb.Worksheets.Item(1).Range("A1").Value2 = "secret"
    $wb.SaveAs((Join-Path $Out 'excel97_encrypted.xls'), 56, 'oa-test-pass'); $wb.Close($false)
} finally { $x.Quit() }

$p = New-Object -ComObject PowerPoint.Application
try {
    function New-Deck($p) {
        $pr = $p.Presentations.Add(0)
        $s = $pr.Slides.Add(1, 2)     # ppLayoutText
        $s.Shapes.Item(1).TextFrame.TextRange.Text = "2026 年度工作汇报"
        $s.Shapes.Item(2).TextFrame.TextRange.Text = "第一点：采购进度`r第二点：Budget in USD`r第三点：风险与对策"
        $s.NotesPage.Shapes.Item(2).TextFrame.TextRange.Text = "讲稿备注 speaker-notes-marker"
        $s2 = $pr.Slides.Add(2, 12)   # blank
        $tb = $s2.Shapes.AddTextbox(1, 50, 50, 400, 60); $tb.TextFrame.TextRange.Text = "第二页文本框 Textbox-Marker"
        $tbl = $s2.Shapes.AddTable(2, 2, 50, 150, 400, 80)
        $tbl.Table.Cell(1, 1).Shape.TextFrame.TextRange.Text = "列A"; $tbl.Table.Cell(1, 2).Shape.TextFrame.TextRange.Text = "列B"
        $tbl.Table.Cell(2, 1).Shape.TextFrame.TextRange.Text = "10"; $tbl.Table.Cell(2, 2).Shape.TextFrame.TextRange.Text = "20"
        return $pr
    }
    $pr = New-Deck $p
    $pr.SaveAs((Join-Path $Out 'ppt97_deck.ppt'), 1)                     # ppSaveAsPowerPoint97
    $pr.SaveAs((Join-Path $Out 'deck.pptx'), 24)                         # ppSaveAsOpenXMLPresentation
    $pr.SaveAs((Join-Path $Out 'deck.odp'), 35)                          # ppSaveAsOpenDocumentPresentation
    $pr.Close()
    $pr = $p.Presentations.Add(0); $pr.Slides.Add(1, 12) | Out-Null
    $pr.Slides.Item(1).Shapes.AddTextbox(1, 10, 10, 200, 40).TextFrame.TextRange.Text = "secret"
    $pr.Password = 'oa-test-pass'
    $pr.SaveAs((Join-Path $Out 'ppt97_encrypted.ppt'), 1); $pr.Close()
} finally { $p.Quit() }
"done"
