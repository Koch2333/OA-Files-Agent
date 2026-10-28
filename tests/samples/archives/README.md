# Archive fixtures

Real 7-Zip files used by `backend/internal/imports/archive_expand_test.go`.

- `sample-lzma2.7z`: a normal LZMA2 archive; expanding it yields the files inside.
- `sample-encrypted.7z`: AES-encrypted; it must stay an ordinary file (no password is ever asked or guessed).

Both are copied unchanged (as `lzma2.7z` and `aes7z.7z`) from the test data of `github.com/bodgit/sevenzip` v1.6.5 (BSD 3-Clause,
Copyright (c) Matt Dainty). The RAR5 test file is generated in the test itself (stored method, valid CRCs), so no binary RAR is checked in.
