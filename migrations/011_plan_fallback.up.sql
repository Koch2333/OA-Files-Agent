BEGIN;
-- Plan items whose department/type could not be determined get the placeholder 其他 and are flagged here
-- (comma separated: department,document_type) until a person confirms or changes them.
ALTER TABLE archive_plan_items ADD COLUMN fallback_fields TEXT NOT NULL DEFAULT '';
COMMIT;
