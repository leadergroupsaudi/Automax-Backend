package services

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Reference pattern grammar (spec §7.9). A pattern is dash-separated segments.
//
// Own-sequence pattern: PREFIX... "YYYY" "N+", for example IN-YYYY-NNNN.
//   - PREFIX segments are literal alphanumerics (one or more: EO-DIR-...).
//   - YYYY is the four-digit year.
//   - a run of N is the sequence, zero-padded to the run's width
//     (NNNN = 4 digits, NN = 2). A sequence wider than the run is printed in
//     full, never truncated, so a busy year cannot produce a duplicate.
//
// Parent-derived pattern: the same text as the parent's pattern, followed by one
// suffix segment of letters then "#", for example IN-YYYY-NNNN-R# or
// CMT-YYYY-NN-M#-I#. The rendered reference is the parent's reference, "-", and
// the final segment with # replaced by the sequence, unpadded (R2, A10). The
// earlier segments are documentation of the shape: the parent supplies them, so
// the year comes from the parent and the year argument is ignored.
//
// A pattern is parent-derived exactly when its last segment contains "#".
var (
	eoSuffixSegment = regexp.MustCompile(`^[A-Za-z]+#$`)
	eoSeqSegment    = regexp.MustCompile(`^N+$`)
	eoLiteralSeg    = regexp.MustCompile(`^[A-Za-z0-9]+$`)
)

type eoParsedPattern struct {
	derived  bool
	segments []string // all segments of the pattern
}

func parseEOPattern(pattern string) (eoParsedPattern, error) {
	if pattern == "" {
		return eoParsedPattern{}, fmt.Errorf("reference pattern is empty")
	}
	segs := strings.Split(pattern, "-")
	last := segs[len(segs)-1]

	if strings.Contains(last, "#") {
		if !eoSuffixSegment.MatchString(last) {
			return eoParsedPattern{}, fmt.Errorf("pattern %q: suffix segment %q must be letters followed by a single #", pattern, last)
		}
		if len(segs) < 2 {
			return eoParsedPattern{}, fmt.Errorf("pattern %q: a parent-derived pattern needs the parent's segments before the suffix", pattern)
		}
		return eoParsedPattern{derived: true, segments: segs}, nil
	}

	// Own sequence: PREFIX... YYYY N+
	if len(segs) < 3 || !eoSeqSegment.MatchString(last) || segs[len(segs)-2] != "YYYY" {
		return eoParsedPattern{}, fmt.Errorf("pattern %q: an own-sequence pattern must end with YYYY then a run of N", pattern)
	}
	for _, s := range segs[:len(segs)-2] {
		if !eoLiteralSeg.MatchString(s) || s == "YYYY" || eoSeqSegment.MatchString(s) {
			return eoParsedPattern{}, fmt.Errorf("pattern %q: prefix segment %q must be a literal", pattern, s)
		}
	}
	return eoParsedPattern{segments: segs}, nil
}

// RenderReference renders a record reference from its type's pattern. year is
// used only by own-sequence patterns; parentRef only by parent-derived ones.
// seq is the 1-based value from EOSequenceRepository.NextInScope.
func RenderReference(pattern string, parentRef string, year int, seq int) (string, error) {
	p, err := parseEOPattern(pattern)
	if err != nil {
		return "", err
	}
	if seq < 1 {
		return "", fmt.Errorf("sequence must be at least 1, got %d", seq)
	}

	if p.derived {
		if parentRef == "" {
			return "", fmt.Errorf("pattern %q is parent-derived but no parent reference was given", pattern)
		}
		if got, want := len(strings.Split(parentRef, "-")), len(p.segments)-1; got != want {
			return "", fmt.Errorf("pattern %q expects a parent reference of %d segments, %q has %d", pattern, want, parentRef, got)
		}
		suffix := strings.TrimSuffix(p.segments[len(p.segments)-1], "#")
		return parentRef + "-" + suffix + strconv.Itoa(seq), nil
	}

	if year < 1000 || year > 9999 {
		return "", fmt.Errorf("year %d is not a four-digit year", year)
	}
	width := len(p.segments[len(p.segments)-1])
	out := make([]string, len(p.segments))
	copy(out, p.segments)
	out[len(out)-2] = strconv.Itoa(year)
	out[len(out)-1] = fmt.Sprintf("%0*d", width, seq)
	return strings.Join(out, "-"), nil
}

// EOSequenceScope names the counter a reference draws its sequence from, for
// EOSequenceRepository.NextInScope. The scope is "<pattern>|<partition>": the
// partition is the parent's reference for a derived pattern (each parent counts
// its own children) and the year for an own-sequence one (the counter restarts
// each year). "|" cannot occur in a valid pattern or reference, so two
// different (pattern, partition) pairs can never yield the same string.
func EOSequenceScope(pattern string, parentRef string, year int) (string, error) {
	p, err := parseEOPattern(pattern)
	if err != nil {
		return "", err
	}
	if p.derived {
		if parentRef == "" {
			return "", fmt.Errorf("pattern %q is parent-derived but no parent reference was given", pattern)
		}
		return pattern + "|" + parentRef, nil
	}
	return pattern + "|" + strconv.Itoa(year), nil
}

// EOParentRefFits reports whether pattern can render against ref: the same
// test RenderReference applies, asked as a question. A pattern that is not
// parent-derived has no parent to fit and reports false. The expectation is
// read from the pattern (its segments before the suffix), never hard-coded.
func EOParentRefFits(pattern, ref string) (bool, error) {
	p, err := parseEOPattern(pattern)
	if err != nil {
		return false, err
	}
	if !p.derived || ref == "" {
		return false, nil
	}
	return len(strings.Split(ref, "-")) == len(p.segments)-1, nil
}
