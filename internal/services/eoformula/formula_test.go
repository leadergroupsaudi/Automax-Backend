package eoformula

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/expr-lang/expr/builtin"
)

func TestEvaluateArithmeticOverMixedBag(t *testing.T) {
	visit := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	env := map[string]any{
		"weeks_a": 4.0,
		"weeks_b": 6,
		"name":    "Tower",
		"active":  true,
		"visit":   visit,
		"done":    3.0,
		"total":   4.0,
	}
	tests := []struct {
		name string
		expr string
		want any
	}{
		{"sum of float and int", "weeks_a + weeks_b", 10.0},
		{"completion rate", "done / total * 100", 75.0},
		{"comparison", "weeks_a < weeks_b", true},
		{"bool logic", "active and weeks_a > 1", true},
		{"string equality", `name == "Tower"`, true},
		{"min helper", "min(weeks_a, weeks_b)", 4.0},
		{"round helper", "round(done / total * 10)", 8.0},
		{"date comparison", "visit > date(\"2026-01-01\")", true},
		{"ternary", `weeks_a > 10 ? "long" : "short"`, "short"},
	}
	ev := New(time.Second)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ev.Eval(tt.expr, env)
			if err != nil {
				t.Fatalf("Eval(%q) error: %v", tt.expr, err)
			}
			if gf, ok := got.(float64); ok {
				wf, _ := tt.want.(float64)
				if math.Abs(gf-wf) > 1e-9 {
					t.Errorf("Eval(%q) = %v, want %v", tt.expr, got, tt.want)
				}
				return
			}
			if got != tt.want {
				t.Errorf("Eval(%q) = %v (%T), want %v", tt.expr, got, got, tt.want)
			}
		})
	}
}

func TestEvalBool(t *testing.T) {
	ev := New(time.Second)
	got, err := ev.EvalBool("a > 1", map[string]any{"a": 2.0})
	if err != nil || !got {
		t.Fatalf("EvalBool = %v, %v; want true, nil", got, err)
	}
	if _, err := ev.EvalBool("a + 1", map[string]any{"a": 2.0}); err == nil {
		t.Error("EvalBool of a non-boolean expression: want error")
	}
}

func TestEvaluateUnresolvedReferenceIsAnError(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		env     map[string]any
		wantKey string
	}{
		{"missing divisor", "a / b", map[string]any{"a": 10.0}, "b"},
		{"missing in comparison", "x > 1", map[string]any{}, "x"},
		{"missing inside function call", "min(a, zz)", map[string]any{"a": 1.0}, "zz"},
		{"missing inside ternary branch", "a > 0 ? 1 : nope", map[string]any{"a": 1.0}, "nope"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(time.Second).Eval(tt.expr, tt.env)
			if !errors.Is(err, ErrUnresolvedReference) {
				t.Fatalf("err = %v, want ErrUnresolvedReference", err)
			}
			if !strings.Contains(err.Error(), tt.wantKey) {
				t.Errorf("err = %q, want it to name %q", err, tt.wantKey)
			}
		})
	}
}

func TestEvaluatePresentNilIsNotUnresolved(t *testing.T) {
	got, err := New(time.Second).EvalBool("x == nil", map[string]any{"x": nil})
	if err != nil || !got {
		t.Fatalf("EvalBool = %v, %v; want true, nil", got, err)
	}
}

func TestEvaluateEnvEscapeIsRejected(t *testing.T) {
	// $env["b"] would bypass identifier checking if it were allowed.
	_, err := New(time.Second).Eval(`$env["b"]`, map[string]any{"a": 1.0})
	if err == nil {
		t.Fatal("$env access: want error")
	}
}

func TestEvaluateRejectsNonFinite(t *testing.T) {
	tests := []struct {
		name string
		expr string
	}{
		{"divide by zero", "a / b"},
		{"negative over zero", "-a / b"},
		{"zero over zero", "b / b"},
		{"int of infinity", "int(a / b)"},
		{"int of huge power", "int(9 ** 9 ** 9)"},
		{"float of infinity", "float(a / b)"},
	}
	env := map[string]any{"a": 1.0, "b": 0.0}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := New(time.Second).Eval(tt.expr, env)
			if err == nil {
				t.Fatalf("Eval(%q) = %v, want error", tt.expr, got)
			}
			if errors.Is(err, ErrUnresolvedReference) {
				t.Errorf("err = %v, should not be an unresolved reference", err)
			}
		})
	}
}

func TestEvaluateBuiltinsAreDisabled(t *testing.T) {
	env := map[string]any{"s": "hello", "n": 1.0}
	tests := []string{
		`len(s)`, `upper(s)`, `trim(s)`, `split(s, "l")`, `replace(s, "l", "x")`,
		`filter([1,2,3], # > 1)`, `map([1,2,3], # * 2)`, `reduce([1,2,3], # + #acc)`,
		`sort([3,1,2])`, `sortBy([1,2], #)`, `all([1], # > 0)`, `sum([1,2])`, `count([1])`,
		`toJSON(s)`, `fromJSON(s)`, `now()`, `keys({})`, `string(n)`, `type(n)`,
		`mean([1,2])`, `join([], ",")`, `repeat(s, 3)`,
	}
	for _, expr := range tests {
		t.Run(expr, func(t *testing.T) {
			if got, err := New(time.Second).Eval(expr, env); err == nil {
				t.Errorf("Eval(%q) = %v, want a compile error", expr, got)
			}
		})
	}
}

func TestAllowedBuiltinsAreExactlyTheAllowlist(t *testing.T) {
	// Every allowlisted name works; this pins the reachable surface.
	env := map[string]any{"x": -2.5}
	for name, expr := range map[string]string{
		"abs": "abs(x)", "ceil": "ceil(x)", "floor": "floor(x)", "round": "round(x)",
		"min": "min(x, 1)", "max": "max(x, 1)", "int": "int(x)", "float": "float(1)",
		"date": `date("2026-01-01")`, "days": "days(1)", "weeks": "weeks(1)",
		"hours": "hours(1)", "days_between": `days_between(date("2026-01-01"), date("2026-01-03"))`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(time.Second).Eval(expr, env); err != nil {
				t.Errorf("Eval(%q) error: %v", expr, err)
			}
		})
	}
}

func TestEvaluateHonoursTimeout(t *testing.T) {
	release := make(chan struct{})
	defer close(release)

	start := time.Now()
	_, err := runWithTimeout(50*time.Millisecond, func() (any, error) {
		<-release
		return 1.0, nil
	})
	elapsed := time.Since(start)

	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("err = %v, want ErrTimeout", err)
	}
	if elapsed > 2*time.Second {
		t.Errorf("returned after %v, want close to the 50ms deadline", elapsed)
	}
}

func TestRunWithTimeoutRecoversPanic(t *testing.T) {
	_, err := runWithTimeout(time.Second, func() (any, error) { panic("boom") })
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("err = %v, want it to carry the panic", err)
	}
}

func TestEvaluateDateArithmetic(t *testing.T) {
	visit := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC)
	env := map[string]any{"visit_date": visit, "notice_days": 1.0}
	ev := New(time.Second)

	got, err := ev.Eval("visit_date - days(2)", env)
	if err != nil {
		t.Fatalf("freeze_date: %v", err)
	}
	want := time.Date(2026, 3, 8, 0, 0, 0, 0, time.UTC)
	if gt, ok := got.(time.Time); !ok || !gt.Equal(want) {
		t.Errorf("freeze_date = %v, want %v", got, want)
	}

	got, err = ev.Eval("visit_date + weeks(2)", env)
	if err != nil {
		t.Fatalf("deadline: %v", err)
	}
	want = time.Date(2026, 3, 24, 0, 0, 0, 0, time.UTC)
	if gt, ok := got.(time.Time); !ok || !gt.Equal(want) {
		t.Errorf("deadline = %v, want %v", got, want)
	}

	n, err := ev.Eval(`days_between(date("2026-03-07"), visit_date)`, env)
	if err != nil || n != 3.0 {
		t.Errorf("days_between = %v, %v; want 3", n, err)
	}

	short, err := ev.EvalBool("notice_days < 2", env)
	if err != nil || !short {
		t.Errorf("short_notice = %v, %v; want true", short, err)
	}
}

func TestEvaluateEmptyExpression(t *testing.T) {
	if _, err := New(time.Second).Eval("   ", nil); err == nil {
		t.Error("empty expression: want error")
	}
}

func TestEveryNonAllowlistedBuiltinIsRejected(t *testing.T) {
	// expr v1.17.8 ignores DisableBuiltin for predicate builtins, so this pins
	// the AST allowlist check against the library's full builtin list.
	allowed := map[string]bool{}
	for _, n := range allowedBuiltins {
		allowed[n] = true
	}
	for _, name := range builtin.Names {
		if allowed[name] || helperNames[name] {
			continue
		}
		t.Run(name, func(t *testing.T) {
			for _, expr := range []string{name + "(n)", name + "(n, n)", name + "([1], # > 0)", name + "()"} {
				if got, err := New(time.Second).Eval(expr, map[string]any{"n": 1.0}); err == nil {
					t.Errorf("Eval(%q) = %v, want error", expr, got)
				}
			}
		})
	}
}

type secretHolder struct{}

func (secretHolder) Reveal() string { return "pwned" }

func TestEvaluateRejectsMethodCallsAndRange(t *testing.T) {
	env := map[string]any{
		"visit_date": time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC),
		"secret":     secretHolder{},
		"rec":        map[string]any{"b": 2.0},
	}
	for _, expr := range []string{
		`visit_date.Format("2006-01-02")`,
		`visit_date.AddDate(1, 0, 0)`,
		`visit_date.String()`,
		`visit_date.Location().String()`,
		`date("2026-01-01").Format("2006")`,
		`secret.Reveal()`,
		`1..100000`,
	} {
		t.Run(expr, func(t *testing.T) {
			if got, err := New(time.Second).Eval(expr, env); err == nil {
				t.Errorf("Eval(%q) = %v, want error", expr, got)
			}
		})
	}
	// Plain member access is not a call and keeps working.
	for _, expr := range []string{`rec.b`, `rec["b"]`} {
		if got, err := New(time.Second).Eval(expr, env); err != nil || got != 2.0 {
			t.Errorf("Eval(%q) = %v, %v; want 2", expr, got, err)
		}
	}
}

func TestZeroValueEvaluatorHasDeadline(t *testing.T) {
	if got, err := (&Evaluator{}).Eval("1 + 1", nil); err != nil || got != 2 {
		t.Fatalf("zero-value Eval = %v, %v", got, err)
	}
}
