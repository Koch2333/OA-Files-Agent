package store

import (
	"context"
	"fmt"

	"oa-agent/backend/internal/archive"
)

// classificationLimit bounds how many distinct values one source contributes before merging.
const classificationLimit = 500

// ClassificationUsage returns the distinct non-empty department (or document type) values already used in
// the ledger, most used first, and, when taskID is not empty, the values present in that task's plans.
// col must be "department" or "document_type".
func (s *Store) ClassificationUsage(ctx context.Context, col, taskID string) (ledger, plan []archive.UsedValue, err error) {
	if col != "department" && col != "document_type" {
		return nil, nil, fmt.Errorf("invalid classification column %q", col)
	}
	scan := func(q string, args ...any) ([]archive.UsedValue, error) {
		rows, err := s.Pool.Query(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out []archive.UsedValue
		for rows.Next() {
			var u archive.UsedValue
			if err := rows.Scan(&u.Value, &u.Count); err != nil {
				return nil, err
			}
			out = append(out, u)
		}
		return out, rows.Err()
	}
	ledger, err = scan(fmt.Sprintf(`SELECT %[1]s, count(*) FROM ledger_entries WHERE status='archived' AND %[1]s<>'' GROUP BY %[1]s ORDER BY count(*) DESC, %[1]s LIMIT $1`, col), classificationLimit)
	if err != nil || taskID == "" {
		return ledger, nil, err
	}
	plan, err = scan(fmt.Sprintf(`SELECT i.%[1]s, count(*) FROM archive_plan_items i JOIN archive_plans p ON p.id=i.plan_id WHERE p.task_id=$1 AND i.%[1]s IS NOT NULL AND i.%[1]s<>'' GROUP BY i.%[1]s ORDER BY count(*) DESC, i.%[1]s LIMIT $2`, col), taskID, classificationLimit)
	return ledger, plan, err
}
