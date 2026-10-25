'use strict'
// Native "choose files / folder" dialog for the local-path import (no HTTP upload, no size limit). The renderer only passes
// { folders }; everything else (dialog properties, filters, the shape of the answer) is fixed here and unit-tested.
const path = require('path')

const FILTERS = [{ name: 'Word / Excel / PDF / 图片', extensions: ['docx', 'xlsx', 'pdf', 'png', 'jpg', 'jpeg'] }, { name: '所有文件', extensions: ['*'] }]

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
