package fileops

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"oa-agent/backend/internal/fsman"
)

// Names of the verification entries inside every ZIP.
const (
	ManifestName = "MANIFEST.sha256"
	NotesName    = "MANIFEST-NOTES.txt"
)

// DownloadPlan is the validated outcome of PlanDownload.
type DownloadPlan struct {
	Roots      []fsman.Entry
	Name       string // suggested download name
	SingleFile bool   // exactly one regular file: serve it as today, no ZIP
}

// PlanDownload validates every selected path through fsman and names the download: a single file is served
// as is; a single folder becomes <folder>.zip; anything else oa-files-<timestamp>.zip. Items below another
// selected folder and duplicates are dropped (they are in the ZIP already).
func (s *Service) PlanDownload(paths []string, now time.Time) (DownloadPlan, error) {
	var plan DownloadPlan
	if len(paths) == 0 {
		return plan, fmt.Errorf("%w: nothing selected", fsman.ErrInvalid)
	}
	seen := map[string]bool{}
	var entries []fsman.Entry
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		e, err := s.FS.Stat(p)
		if err != nil {
			return plan, err
		}
		if s.FS.IsSymlink(p) {
			return plan, fmt.Errorf("%w: symbolic links are not managed", fsman.ErrInvalid)
		}
		entries = append(entries, e)
	}
	for _, e := range entries {
		covered := false
		for _, o := range entries {
			if o.IsDir && inside(e.Path, o.Path) {
				covered = true
			}
		}
		if !covered {
			plan.Roots = append(plan.Roots, e)
		}
	}
	switch {
	case len(plan.Roots) == 1 && !plan.Roots[0].IsDir:
		plan.SingleFile, plan.Name = true, plan.Roots[0].Name
	case len(plan.Roots) == 1:
		plan.Name = plan.Roots[0].Name + ".zip"
	default:
		plan.Name = "oa-files-" + now.Format("20060102-150405") + ".zip"
	}
	return plan, nil
}

// ZipReport describes a finished (or aborted) archive stream.
type ZipReport struct {
	Files    int
	Dirs     int
	Bytes    int64
	Skipped  []string // vanished or unreadable while streaming
	Mismatch []string // task-related files whose streamed content differs from the ledger hash
}

// zipSafeName makes one path segment safe for every extractor: no separators, control characters, or
// invalid UTF-8 (Go sets the UTF-8 flag for non-ASCII names).
func zipSafeName(seg string) string {
	seg = strings.ToValidUTF8(seg, "_")
	var b strings.Builder
	for _, r := range seg {
		switch {
		case r == '\\' || r == '/' || r < 0x20 || r == 0x7f || r == utf8.RuneError:
			b.WriteByte('_')
		default:
			b.WriteRune(r)
		}
	}
	out := b.String()
	if out == "" || out == "." || out == ".." {
		return "_"
	}
	return out
}

func zipMethod(name string) uint16 {
	switch strings.ToLower(path.Ext(name)) {
	case ".pdf", ".png", ".jpg", ".jpeg", ".jfif", ".webp", ".gif", ".zip", ".docx", ".xlsx", ".pptx", ".odt", ".ods", ".odp", ".gz", ".7z", ".mp4":
		return zip.Store // already compressed
	}
	return zip.Deflate
}

// WriteZip streams the planned selection as a ZIP to w without building it anywhere first: each file is read
// once, hashed while it is written, and a MANIFEST.sha256 (sha256sum -c format, paths relative to the archive
// root) follows at the end. Everything comes from fsman-validated relative paths: entry names are rebuilt from
// directory entries (never from user text), symbolic links and hidden internal folders are skipped, and
// ordering is deterministic. ctx cancellation (client abort) stops reading immediately; a write error aborts
// without finalising the archive so a truncated download can never pass for a complete one.
func (s *Service) WriteZip(ctx context.Context, w io.Writer, plan DownloadPlan, actor string) (rep ZipReport, err error) {
	defer func() {
		state := "完成"
		if err != nil {
			state = "中断"
		}
		s.Store.Audit(context.WithoutCancel(ctx), actor, "fs.download", fmt.Sprintf("%d 项", len(plan.Roots)), fmt.Sprintf("打包下载%s：文件 %d 个、文件夹 %d 个、%d 字节", state, rep.Files, rep.Dirs, rep.Bytes))
	}()
	zw := zip.NewWriter(w)
	type line struct{ sha, name, src string }
	var lines []line
	used := map[string]bool{strings.ToLower(ManifestName): true, strings.ToLower(NotesName): true}
	top := func(name string) string {
		name = zipSafeName(name)
		cand := name
		for n := 2; used[strings.ToLower(cand)]; n++ {
			ext := path.Ext(name)
			cand = fmt.Sprintf("%s (%d)%s", strings.TrimSuffix(name, ext), n, ext)
		}
		used[strings.ToLower(cand)] = true
		return cand
	}
	copyFile := func(e fsman.Entry, entry string) error {
		f, fi, oerr := s.FS.Open(e.Path)
		if oerr != nil {
			if errors.Is(oerr, fsman.ErrNotFound) || errors.Is(oerr, fsman.ErrInvalid) {
				rep.Skipped = append(rep.Skipped, e.Path)
				return nil
			}
			return oerr
		}
		defer f.Close()
		hdr := &zip.FileHeader{Name: entry, Method: zipMethod(entry), Modified: fi.ModTime()}
		hdr.SetMode(0o644)
		hdr.UncompressedSize64 = uint64(fi.Size()) // hint: lets archive/zip emit ZIP64 records for entries over 4 GiB
		zf, cerr := zw.CreateHeader(hdr)
		if cerr != nil {
			return cerr
		}
		h := sha256.New()
		n, cperr := io.Copy(io.MultiWriter(zf, h), fsman.CtxReader(ctx, f))
		rep.Bytes += n
		if cperr != nil {
			return cperr
		}
		rep.Files++
		lines = append(lines, line{hex.EncodeToString(h.Sum(nil)), entry, e.Path})
		return nil
	}
	addDir := func(entry string, mod time.Time) error {
		hdr := &zip.FileHeader{Name: entry + "/", Method: zip.Store, Modified: mod}
		hdr.SetMode(fs.ModeDir | 0o755)
		if _, cerr := zw.CreateHeader(hdr); cerr != nil {
			return cerr
		}
		rep.Dirs++
		return nil
	}
	for _, root := range plan.Roots {
		if err = ctx.Err(); err != nil {
			return rep, err
		}
		name := top(root.Name)
		if !root.IsDir {
			if err = copyFile(root, name); err != nil {
				return rep, err
			}
			continue
		}
		if err = addDir(name, root.ModifiedAt); err != nil {
			return rep, err
		}
		used2 := map[string]bool{}
		err = s.FS.Walk(ctx, root.Path, func(e fsman.Entry) error {
			rel := strings.TrimPrefix(e.Path, root.Path+"/")
			parts := strings.Split(rel, "/")
			for i := range parts {
				parts[i] = zipSafeName(parts[i])
			}
			entry := name + "/" + strings.Join(parts, "/")
			for n := 2; used2[strings.ToLower(entry)]; n++ { // names that collapsed to the same safe name
				entry = fmt.Sprintf("%s_%d", name+"/"+strings.Join(parts, "/"), n)
			}
			used2[strings.ToLower(entry)] = true
			if e.IsDir {
				return addDir(entry, e.ModifiedAt)
			}
			return copyFile(e, entry)
		})
		if err != nil {
			return rep, err
		}
	}
	// ledger cross-check for task-related files (cheap: one indexed query per 500 paths, after the stream)
	if len(lines) > 0 {
		srcs := make([]string, len(lines))
		for i, l := range lines {
			srcs[i] = l.src
		}
		if rows, lerr := s.Store.LedgerHashes(ctx, srcs); lerr == nil {
			for _, l := range lines {
				if r, ok := rows[l.src]; ok && r.SHA256 != l.sha {
					rep.Mismatch = append(rep.Mismatch, l.name)
				}
			}
		}
	}
	mf, err := zw.CreateHeader(&zip.FileHeader{Name: ManifestName, Method: zip.Deflate, Modified: time.Now()})
	if err != nil {
		return rep, err
	}
	for _, l := range lines {
		if _, err = io.WriteString(mf, l.sha+"  "+l.name+"\n"); err != nil {
			return rep, err
		}
	}
	if len(rep.Skipped)+len(rep.Mismatch) > 0 {
		nf, nerr := zw.CreateHeader(&zip.FileHeader{Name: NotesName, Method: zip.Deflate, Modified: time.Now()})
		if nerr != nil {
			return rep, nerr
		}
		var b strings.Builder
		if len(rep.Mismatch) > 0 {
			b.WriteString("以下任务相关文件的内容与台账记录的 SHA-256 不一致（可能在系统外被修改）；MANIFEST.sha256 中为打包时的实际哈希：\n")
			for _, m := range rep.Mismatch {
				b.WriteString("  " + m + "\n")
			}
		}
		if len(rep.Skipped) > 0 {
			b.WriteString("以下文件在打包期间消失或无法读取，未包含在压缩包中：\n")
			for _, m := range rep.Skipped {
				b.WriteString("  " + m + "\n")
			}
		}
		if _, err = io.WriteString(nf, b.String()); err != nil {
			return rep, err
		}
	}
	if err = zw.Close(); err != nil {
		return rep, err
	}
	return rep, nil
}
