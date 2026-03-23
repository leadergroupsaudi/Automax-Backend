package models

import (
	"database/sql/driver"
	"fmt"
	"strings"
)

// ClassificationTypes is a []string that reads/writes PostgreSQL text[] natively.
// It serialises to JSON as a plain array, identical to []string.
type ClassificationTypes []string

// Value implements driver.Valuer — produces a PostgreSQL text[] literal e.g. {"incident","request"}.
func (ct ClassificationTypes) Value() (driver.Value, error) {
	if len(ct) == 0 {
		return "{}", nil
	}
	b := &strings.Builder{}
	b.WriteByte('{')
	for i, s := range ct {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String(), nil
}

// Scan implements sql.Scanner — parses a PostgreSQL text[] literal back into a Go slice.
func (ct *ClassificationTypes) Scan(value interface{}) error {
	if value == nil {
		*ct = ClassificationTypes{}
		return nil
	}
	var str string
	switch v := value.(type) {
	case string:
		str = v
	case []byte:
		str = string(v)
	default:
		return fmt.Errorf("ClassificationTypes.Scan: unsupported type %T", value)
	}
	parsed, err := parsePGTextArray(str)
	if err != nil {
		return err
	}
	*ct = ClassificationTypes(parsed)
	return nil
}

// parsePGTextArray parses a PostgreSQL text[] literal such as {incident,request} or
// {"incident","request"} into a []string.
func parsePGTextArray(s string) ([]string, error) {
	if s == "" || s == "{}" {
		return []string{}, nil
	}
	if len(s) < 2 || s[0] != '{' || s[len(s)-1] != '}' {
		return nil, fmt.Errorf("parsePGTextArray: invalid format %q", s)
	}
	inner := s[1 : len(s)-1]
	if inner == "" {
		return []string{}, nil
	}

	var result []string
	var current strings.Builder
	inQuote := false
	escaped := false

	for _, ch := range inner {
		switch {
		case escaped:
			current.WriteRune(ch)
			escaped = false
		case ch == '\\':
			escaped = true
		case ch == '"':
			inQuote = !inQuote
		case ch == ',' && !inQuote:
			result = append(result, current.String())
			current.Reset()
		default:
			current.WriteRune(ch)
		}
	}
	result = append(result, current.String())
	return result, nil
}
