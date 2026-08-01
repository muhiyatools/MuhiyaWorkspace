package admin

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lib/pq"
)

// A bare "Internal server error. See gateway logs for details." is the right
// answer for an anonymous caller and the wrong one here: the admin API is
// already authenticated as an operator, and hiding the cause from the one
// person who can fix it turns a one-line schema problem into a debugging
// session against production logs.
//
// describeDatabaseError classifies the failure by Postgres SQLSTATE and returns
// a message that names what to do about it. It deliberately reports structural
// facts (which column, which constraint, which migration) and never row values,
// so nothing a caller supplied is echoed back.
//
// SQLSTATE reference: https://www.postgresql.org/docs/current/errcodes-appendix.html
func describeDatabaseError(err error) (string, bool) {
	var pqErr *pq.Error
	if !errors.As(err, &pqErr) {
		return "", false
	}
	switch pqErr.Code {
	case "42703": // undefined_column
		return fmt.Sprintf(
			"The database is missing a column this gateway build expects (%s). "+
				"Pending migrations have not been applied — restart the gateway so it runs them, "+
				"or apply db/migrations manually, then retry.",
			describeMissingObject(pqErr)), true

	case "42P01": // undefined_table
		return fmt.Sprintf(
			"The database is missing a table this gateway build expects (%s). "+
				"Pending migrations have not been applied — restart the gateway so it runs them, "+
				"or apply db/migrations manually, then retry.",
			describeMissingObject(pqErr)), true

	case "23505": // unique_violation
		return fmt.Sprintf("That value is already taken (%s). Change it and retry.",
			constraintLabel(pqErr)), true

	case "23514": // check_violation
		return fmt.Sprintf("A value failed a database rule (%s). "+
			"Check the pricing tiers, cache lifetimes and price windows on this model.",
			constraintLabel(pqErr)), true

	case "23503": // foreign_key_violation
		return fmt.Sprintf("This references a record that does not exist (%s).",
			constraintLabel(pqErr)), true

	case "23502": // not_null_violation
		return fmt.Sprintf("A required field was empty (%s).",
			describeMissingObject(pqErr)), true

	case "22001": // string_data_right_truncation
		return "One of the values is longer than the column allows.", true

	case "22003": // numeric_value_out_of_range
		return "A numeric value is out of range for its column.", true
	}
	return "", false
}

// describeMissingObject names the column/table at fault, falling back to the
// driver's own message when the structured fields are empty (lib/pq populates
// Column/Table only for some error classes).
func describeMissingObject(pqErr *pq.Error) string {
	var parts []string
	if pqErr.Table != "" {
		parts = append(parts, "table "+pqErr.Table)
	}
	if pqErr.Column != "" {
		parts = append(parts, "column "+pqErr.Column)
	}
	if len(parts) > 0 {
		return strings.Join(parts, ", ")
	}
	// pqErr.Message for these classes reads e.g.
	// `column "prompt_accounting" of relation "models" does not exist`.
	return strings.TrimSpace(pqErr.Message)
}

func constraintLabel(pqErr *pq.Error) string {
	if pqErr.Constraint != "" {
		return "constraint " + pqErr.Constraint
	}
	if pqErr.Column != "" {
		return "column " + pqErr.Column
	}
	return strings.TrimSpace(pqErr.Message)
}
