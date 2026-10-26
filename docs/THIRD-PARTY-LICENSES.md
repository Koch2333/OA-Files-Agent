# 第三方组件与许可证

| 组件 | 用途 | 许可证 |
|---|---|---|
| PostgreSQL 16 | 内置数据库 | PostgreSQL License |
| pgx、excelize、golang.org/x/* | Go 后端 | MIT / BSD-3-Clause |
| Vue、Vue Router、Pinia、Vite | 前端 | MIT |
| @material/material-color-utilities | 主题配色 | Apache-2.0 |
| Material Symbols（@material-symbols/svg-400，生成为内联 SVG 路径） | 图标 | Apache-2.0 |
| python-docx、openpyxl | Word/Excel（.docx/.xlsx）解析 | MIT |
| olefile | Office 97-2003（.doc/.ppt/.xls/.msg）所用的 OLE2 复合文档读取 | BSD-2-Clause |
| xlrd（2.x，只读 .xls） | Excel 97-2003（.xls）解析 | BSD-3-Clause |
| 无新增依赖（自写，标准库） | .doc / .ppt 文本抽取、RTF、OpenDocument、PPTX、HTML、EML、CSV 编码识别 | 本项目 AGPL-3.0 |
| **PyMuPDF** | PDF 文本提取与渲染 | **AGPL-3.0**（商业分发需遵守 AGPL 或向 Artifex 购买商业许可；对外发行前必须评审） |
| RapidOCR（PaddleOCR 模型的 ONNX 版）、ONNX Runtime、OpenCV | OCR | Apache-2.0 / MIT / Apache-2.0 |
| NumPy、Pillow | 图像处理 | BSD / HPND |
| PyInstaller | 打包 Worker | GPL-2.0 + 打包例外（产物不受 GPL 约束） |

| minio-go、gofakes3（测试） | S3 兼容存储 | Apache-2.0 / MIT |
| azure-sdk-for-go（azblob） | Azure Blob | MIT |
| pkg/sftp、x/crypto/ssh | SFTP | BSD-2/3-Clause |
| jlaffaye/ftp | FTP/FTPS | ISC |
| cloudsoda/go-smb2 | SMB | BSD-2-Clause |
| **OpenList**（部分云盘 API 调用逻辑改编，见 `backend/internal/cloud/*` 中带 “Adapted from OpenList” 头注释的文件） | 云盘/私有云存储提供方 | **AGPL-3.0** |

## 本项目许可证

本项目以 **AGPL-3.0** 发布（见仓库根目录 `LICENSE`）：改编自 OpenList（AGPL-3.0）的代码要求整体采用兼容的 AGPL-3.0；PyMuPDF 同为 AGPL-3.0，与之一致。对外提供网络服务时须向用户提供对应源码。依赖库的许可证（MIT/BSD/Apache-2.0/ISC）与 AGPL-3.0 兼容。

此清单为人工整理，正式交付前请用许可证扫描工具复核。
