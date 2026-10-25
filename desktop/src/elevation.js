'use strict'
// Windows UIPI blocks drag-and-drop from a normal (Explorer) process into an ELEVATED window, silently: the drop event never
// fires. So the app has to know if it runs as administrator and tell the user. Detection: fltmc.exe needs administrator
// rights (exit 0 elevated, non-zero "access denied" otherwise); it is a read-only listing of filter drivers.
const { execFile } = require('child_process')
const path = require('path')

function isElevated(opts = {}) {
  const platform = opts.platform || process.platform
  const run = opts.execFile || execFile
  if (platform !== 'win32') return Promise.resolve(false) // the problem is Windows-specific
  const exe = path.join(process.env.SystemRoot || 'C:\\Windows', 'System32', 'fltmc.exe')
  return new Promise((resolve) => {
    try {
      run(exe, [], { windowsHide: true, timeout: 5000 }, (err) => resolve(!err))
    } catch {
      resolve(false) // cannot tell: do not nag
    }
  })
}
module.exports = { isElevated }
