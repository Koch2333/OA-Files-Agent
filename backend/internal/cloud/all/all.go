// Package all links every built-in storage provider into the registry. Import it for its side effects.
package all

import (
	_ "oa-agent/backend/internal/cloud/aliyunpan"
	_ "oa-agent/backend/internal/cloud/azureblob"
	_ "oa-agent/backend/internal/cloud/baidupan"
	_ "oa-agent/backend/internal/cloud/cloudreve"
	_ "oa-agent/backend/internal/cloud/dingtalk"
	_ "oa-agent/backend/internal/cloud/dropbox"
	_ "oa-agent/backend/internal/cloud/ftp"
	_ "oa-agent/backend/internal/cloud/googledrive"
	_ "oa-agent/backend/internal/cloud/localdir"
	_ "oa-agent/backend/internal/cloud/onedrive"
	_ "oa-agent/backend/internal/cloud/openlist"
	_ "oa-agent/backend/internal/cloud/pan123"
	_ "oa-agent/backend/internal/cloud/s3"
	_ "oa-agent/backend/internal/cloud/seafile"
	_ "oa-agent/backend/internal/cloud/sftp"
	_ "oa-agent/backend/internal/cloud/smb"
	_ "oa-agent/backend/internal/cloud/webdav"
)
