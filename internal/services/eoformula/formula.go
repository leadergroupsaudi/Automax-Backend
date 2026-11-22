// Package eoformula evaluates the formulas attached to EO record-type fields
// (spec §7.5): arithmetic, comparison, dates and aggregates over a record's own
// values, its children's, or a named ancestor's.
//
// The language is deliberately small. It is not a general automation language:
// there is no I/O, no loops or closures, and evaluation is bounded by a
// timeout, a node budget and expr's memory budget.
//
// Unlike metricformula (float64-only, no timeout, all expr builtins reachable,
// missing names silently 0), this package evaluates over a mixed-type value bag
// (numbers, strings, booleans, time.Time), exposes only the allowlist below,
// and treats a reference to a name absent from the env as an error.
//
// Language surface:
//
//	operators:  + - * / % **  == != < <= > >=  and or not  ?:  in
//	numeric:    abs ceil floor round min max int float
//	dates:      date("2026-03-10")  days(n) weeks(n) hours(n)  days_between(a, b)
//
// A duration is written with a unit function: "visit_date - days(2)" is two
// days before visit_date, "start + weeks(6)" six weeks after start. Subtracting
// two dates yields a duration; use days_between(a, b) for a plain number of
// days. Aggregates over children (sum, count, ...) are not language features:
// the caller puts them in env as ordinary values (e.g. children_sum_weeks).
package eoformula

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/expr-lang/expr"
	"github.com/expr-lang/expr/ast"
)

// ErrUnresolvedReference is returned when a formula names a value that is not
// in the env. errors.Is matches; the message names the missing key(s).
var ErrUnresolvedReference = errors.New("eoformula: unresolved reference")

// ErrTimeout is returned when evaluation exceeds the evaluator's Timeout.
var ErrTimeout = errors.New("eoformula: evaluation timed out")

// DefaultTimeout is used when New is given a non-positive timeout.
const DefaultTimeout = 500 * time.Millisecond

// maxNodes bounds formula size (expr's default is 10000; formulas are tiny).
const maxNodes = 500

// allowedBuiltins is the whole set of expr builtins a formula can reach. Every
// other builtin is disabled at compile time.
var allowedBuiltins = []string{"abs", "ceil", "floor", "round", "min", "max", "date"}

// Evaluator evaluates formulas. The zero value has no timeout; use New.
type Evaluator struct {
	Timeout time.Duration
}

// New returns an Evaluator whose evaluations are bounded by timeout
// (DefaultTimeout if timeout <= 0).
func New(timeout time.Duration) *Evaluator {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &Evaluator{Timeout: timeout}
}

// helperNames are the custom functions (not expr builtins) exposed to formulas.
var helperNames = map[string]bool{"int": true, "float": true, "days": true, "weeks": true, "hours": true, "days_between": true}

func compileOptions(env map[string]any) []expr.Option {
	opts := []expr.Option{
		expr.Env(env),
		// Unknown names must reach unresolved() so the error can name them
		// and wrap ErrUnresolvedReference, rather than surface as a compile error.
		expr.AllowUndefinedVariables(),
		// Keep the tree unoptimised so the allowlist check below sees every
		// builtin call; the optimiser can fold calls away before we look.
		expr.Optimize(false),
		expr.MaxNodes(maxNodes),
		expr.DisableAllBuiltins(),
	}
	for _, name := range allowedBuiltins {
		opts = append(opts, expr.EnableBuiltin(name))
	}
	return append(opts,
		expr.Function("days", unitFn("days", 24*time.Hour)),
		expr.Function("weeks", unitFn("weeks", 7*24*time.Hour)),
		expr.Function("hours", unitFn("hours", time.Hour)),
		expr.Function("days_between", daysBetween),
		// int/float replace the builtins: the builtin int() turns +-Inf/NaN into
		// a Go int, which would slip past the non-finite result check.
		expr.Function("int", convFn("int", func(f float64) any { return int(f) })),
		expr.Function("float", convFn("float", func(f float64) any { return f })),
	)
}

func unitFn(name string, unit time.Duration) func(params ...any) (any, error) {
	return func(params ...any) (any, error) {
		if len(params) != 1 {
			return nil, fmt.Errorf("%s() takes 1 argument, got %d", name, len(params))
		}
		n, ok := toFloat(params[0])
		if !ok {
			return nil, fmt.Errorf("%s() needs a number, got %T", name, params[0])
		}
		return time.Duration(n * float64(unit)), nil
	}
}

func convFn(name string, conv func(float64) any) func(params ...any) (any, error) {
	return func(params ...any) (any, error) {
		if len(params) != 1 {
			return nil, fmt.Errorf("%s() takes 1 argument, got %d", name, len(params))
		}
		f, ok := toFloat(params[0])
		if !ok {
			return nil, fmt.Errorf("%s() needs a number, got %T", name, params[0])
		}
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, fmt.Errorf("%s() of a non-finite value", name)
		}
		return conv(f), nil
	}
}

func daysBetween(params ...any) (any, error) {
	if len(params) != 2 {
		return nil, fmt.Errorf("days_between() takes 2 arguments, got %d", len(params))
	}
	a, okA := params[0].(time.Time)
	b, okB := params[1].(time.Time)
	if !okA || !okB {
		return nil, fmt.Errorf("days_between() needs two dates, got %T and %T", params[0], params[1])
	}
	return b.Sub(a).Hours() / 24, nil
}

func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}

// refCollector gathers the identifiers a program reads from the env.
type refCollector struct {
	idents  []*ast.IdentifierNode
	callees map[*ast.IdentifierNode]bool
	// banned is the first builtin call outside allowedBuiltins, if any.
	banned string
}

func (c *refCollector) Visit(node *ast.Node) {
	switch n := (*node).(type) {
	case *ast.IdentifierNode:
		c.idents = append(c.idents, n)
	case *ast.BuiltinNode:
		if !allowedBuiltinSet[n.Name] && c.banned == "" {
			c.banned = n.Name
		}
	case *ast.PredicateNode, *ast.PointerNode:
		if c.banned == "" {
			c.banned = "closure"
		}
	case *ast.CallNode:
		// Only calls to our own helpers are allowed. A MemberNode callee is a
		// method call on an env value (visit_date.Format(...)), which expr
		// resolves by reflection at run time.
		if id, ok := n.Callee.(*ast.IdentifierNode); ok && helperNames[id.Value] {
			c.callees[id] = true
		} else if c.banned == "" {
			c.banned = "method call"
		}
	case *ast.BinaryNode:
		if n.Operator == ".." && c.banned == "" {
			c.banned = "range operator"
		}
	}
}

var allowedBuiltinSet = func() map[string]bool {
	m := make(map[string]bool, len(allowedBuiltins))
	for _, n := range allowedBuiltins {
		m[n] = true
	}
	return m
}()

// check walks the compiled program's AST before Run. It rejects any builtin
// outside the allowlist (see note on DisableAllBuiltins below) and returns the
// sorted env keys the program reads that env lacks. A key present with a nil
// value is resolved (so "x == nil" works on a null field).
//
// expr v1.17.8's parser ignores DisableBuiltin for the predicate builtins
// (all, any, filter, map, count, sum, reduce, sortBy, ...): they still parse
// and run. The explicit AST check here is what actually keeps them out.
func check(tree ast.Node, env map[string]any) ([]string, error) {
	c := &refCollector{callees: map[*ast.IdentifierNode]bool{}}
	ast.Walk(&tree, c)
	if c.banned != "" {
		return nil, fmt.Errorf("eoformula: function %s is not available in formulas", c.banned)
	}
	seen := map[string]bool{}
	for _, id := range c.idents {
		if c.callees[id] {
			continue
		}
		if strings.HasPrefix(id.Value, "$") {
			return nil, fmt.Errorf("eoformula: %s is not available in formulas", id.Value)
		}
		if _, ok := env[id.Value]; !ok {
			seen[id.Value] = true
		}
	}
	missing := make([]string, 0, len(seen))
	for k := range seen {
		missing = append(missing, k)
	}
	sort.Strings(missing)
	return missing, nil
}

// Eval compiles and runs expression against env and returns its value. A
// float64 result that is NaN or infinite is an error.
func (e *Evaluator) Eval(expression string, env map[string]any) (any, error) {
	if strings.TrimSpace(expression) == "" {
		return nil, errors.New("eoformula: expression is empty")
	}
	if env == nil {
		env = map[string]any{}
	}

	program, err := expr.Compile(expression, compileOptions(env)...)
	if err != nil {
		return nil, fmt.Errorf("eoformula: compile: %w", err)
	}

	missing, err := check(program.Node(), env)
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrUnresolvedReference, strings.Join(missing, ", "))
	}

	timeout := e.Timeout
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	out, err := runWithTimeout(timeout, func() (any, error) {
		return expr.Run(program, env)
	})
	if err != nil {
		if errors.Is(err, ErrTimeout) {
			return nil, err
		}
		return nil, fmt.Errorf("eoformula: run: %w", err)
	}
	if f, ok := out.(float64); ok && (math.IsNaN(f) || math.IsInf(f, 0)) {
		return nil, errors.New("eoformula: formula produced a non-finite result")
	}
	return out, nil
}

// EvalBool is Eval for conditions; the expression must yield a boolean. It
// satisfies services.FormulaEvaluator.
func (e *Evaluator) EvalBool(expression string, env map[string]any) (bool, error) {
	out, err := e.Eval(expression, env)
	if err != nil {
		return false, err
	}
	b, ok := out.(bool)
	if !ok {
		return false, fmt.Errorf("eoformula: expression did not yield a boolean: got %T", out)
	}
	return b, nil
}

// runWithTimeout runs fn in a goroutine and waits at most d. A zero or negative
// d means no deadline. On timeout the goroutine is NOT stopped: Go cannot kill a
// goroutine, so it runs to completion in the background and its result is
// discarded (the channel is buffered so it does not leak blocked on send).
// The language has no loops and expr bounds memory, so a runaway is bounded by
// the program size, but the CPU is spent regardless.
func runWithTimeout(d time.Duration, fn func() (any, error)) (any, error) {
	type result struct {
		v   any
		err error
	}
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- result{nil, fmt.Errorf("panic: %v", r)}
			}
		}()
		v, err := fn()
		ch <- result{v, err}
	}()
	if d <= 0 {
		r := <-ch
		return r.v, r.err
	}
	select {
	case r := <-ch:
		return r.v, r.err
	case <-time.After(d):
		return nil, ErrTimeout
	}
}
