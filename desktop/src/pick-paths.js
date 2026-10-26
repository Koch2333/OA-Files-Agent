'use strict'
// Native "choose files / folder" dialog for the local-path import (no HTTP upload, no size limit). The renderer only passes
// { folders }; everything else (dialog properties, filters, the shape of the answer) is fixed here and unit-tested.
const path = require('path')

// Extensions whose content is read (contracts/formats.json); any other file is accepted too and archived by its name only.
const FILTERS = [
  { name: '可识别内容的文档 / 图片', extensions: ['docx', 'doc', 'odt', 'rtf', 'xlsx', 'xls', 'ods', 'csv', 'tsv', 'pptx', 'ppt', 'odp', 'pdf', 'png', 'jpg', 'jpeg', 'jfif', 'bmp', 'tif', 'tiff', 'webp', 'gif', 'txt', 'md', 'html', 'htm', 'eml', 'msg'] },
  { name: '所有文件', extensions: ['*'] },
]

function dialogOptions(opts) {
  const folders = !!(opts && opts.folders === true)
  return folders
    ? { title: '选择要导入的文件夹', properties: ['openDirectory', 'multiSelections'] }
    : { title: '选择要导入的文件', properties: ['openFile', 'multiSelections'], filters: FILTERS }
}

/** Dialog result -> list of absolute, normalised path strings ([] when cancelled or malformed). */
function pickedPaths(result) {
  if (!result || result.canceled || !Array.isArray(result.filePaths)) return []
  return result.filePaths.filter((p) => typeof p === 'string' && p !== '' && !p.includes('\0') && path.isAbsolute(p))
}

async function pickPaths(showOpenDialog, opts) {
  return pickedPaths(await showOpenDialog(dialogOptions(opts)))
}

module.exports = { dialogOptions, pickedPaths, pickPaths, FILTERS }
