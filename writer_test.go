package endpoint

import (
	"errors"
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func render(t *testing.T, w *Writer) string {
	t.Helper()
	var b strings.Builder
	n, err := w.WriteTo(&b)
	if err != nil || n != int64(b.Len()) {
		t.Fatalf("WriteTo = %d, %v; wrote %d", n, err, b.Len())
	}
	out := b.String()
	if err := strictCheck(out); err != nil {
		t.Fatalf("output fails the strict check: %v\n%s", err, out)
	}
	return out
}

// strictCheck is a line-level checker for text format 0.0.4, stricter
// than Prometheus: every line must be a HELP, a TYPE, one of our comments
// or a sample; a family's TYPE comes once, before its samples, which are
// contiguous; the input ends with a newline. The real parsers run in
// ./internal/promcheck.
func strictCheck(s string) error {
	if s == "" {
		return nil
	}
	if !strings.HasSuffix(s, "\n") {
		return errors.New("no final newline")
	}
	const (
		name  = `[a-zA-Z_:][a-zA-Z0-9_:]*`
		label = `[a-zA-Z_][a-zA-Z0-9_]*="(?:[^"\\\n]|\\[\\"n])*"`
		value = `(?:[+-]Inf|NaN|-?[0-9]+(?:\.[0-9]+)?(?:e[+-][0-9]+)?)`
	)
	help := regexp.MustCompile(`^# HELP (` + name + `) (?:[^\\\n]|\\[\\n])*$`)
	typ := regexp.MustCompile(`^# TYPE (` + name + `) (counter|gauge)$`)
	sample := regexp.MustCompile(`^(` + name + `)(?:\{` + label + `(?:,` + label + `)*\})? ` + value + `$`)
	ours := regexp.MustCompile(`^# endpoint: skipped (counter|gauge) "`)
	typed := map[string]bool{}
	current, done := "", map[string]bool{}
	for i, line := range strings.Split(strings.TrimSuffix(s, "\n"), "\n") {
		switch {
		case help.MatchString(line):
			n := help.FindStringSubmatch(line)[1]
			if typed[n] {
				return errors.New("HELP after TYPE: " + line)
			}
		case typ.MatchString(line):
			n := typ.FindStringSubmatch(line)[1]
			if typed[n] {
				return errors.New("second TYPE: " + line)
			}
			typed[n] = true
			if current != "" {
				done[current] = true
			}
			current = n
		case sample.MatchString(line):
			n := sample.FindStringSubmatch(line)[1]
			if n != current || done[n] {
				return errors.New("sample outside its family: " + line)
			}
		case ours.MatchString(line):
		default:
			return errors.New("line " + strconv.Itoa(i+1) + " matches nothing: " + line)
		}
	}
	return nil
}

func TestStrictCheckRejects(t *testing.T) {
	// Positive controls: the checker must be able to fail.
	for _, bad := range []string{
		"x 1",
		"# TYPE x gauge\n# TYPE x gauge\nx 1\n",
		"# TYPE x gauge\nx{a=\"\"\"} 1\n",
		"# TYPE x gauge\nx{a=\"\\t\"} 1\n",
		"# TYPE x gauge\ny 1\n",
		"# TYPE x gauge\nx 1\n# TYPE y gauge\ny 1\nx 2\n",
		"# TYPE x gauge\n# HELP x late\nx 1\n",
		"# HELP x a\\tb\n",
		"# TYPE x gauge\nx 1e6\n",
		"# TYPE x summary\n",
		"garbage\n",
	} {
		if strictCheck(bad) == nil {
			t.Errorf("strictCheck accepted %q", bad)
		}
	}
}

func TestGolden(t *testing.T) {
	var w Writer
	w.Counter("http_requests_total", "Requests served.\nBy code.",
		S(3, L("code", "200"), L("method", "GET")),
		S(1, L("code", "500"), L("method", "GET")))
	w.Gauge("up", "", S(1))
	w.Gauge("esc", `back\slash`, S(0, L("v", "a\\b\"c\nd")))
	if err := w.Err(); err != nil {
		t.Fatalf("Err = %v on a clean scrape", err)
	}
	want := `# HELP http_requests_total Requests served.\nBy code.
# TYPE http_requests_total counter
http_requests_total{code="200",method="GET"} 3
http_requests_total{code="500",method="GET"} 1
# TYPE up gauge
up 1
# HELP esc back\\slash
# TYPE esc gauge
esc{v="a\\b\"c\nd"} 0
`
	if got := render(t, &w); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestEmptyWriter(t *testing.T) {
	var w Writer
	if got := render(t, &w); got != "" || w.Err() != nil {
		t.Errorf("empty writer: %q, %v", got, w.Err())
	}
}

func TestFormatValue(t *testing.T) {
	for _, c := range []struct {
		v    float64
		want string
	}{
		{0, "0"},
		{math.Copysign(0, -1), "-0"},
		{1, "1"},
		{-1, "-1"},
		{1e6, "1000000"},
		{123456789, "123456789"},
		{1 << 53, "9007199254740992"},
		{-(1 << 53), "-9007199254740992"},
		{1<<53 + 2, "9.007199254740994e+15"},
		{1e21, "1e+21"},
		{0.5, "0.5"},
		{1e-7, "1e-07"},
		{math.MaxFloat64, "1.7976931348623157e+308"},
		{math.SmallestNonzeroFloat64, "5e-324"},
		{math.Inf(1), "+Inf"},
		{math.Inf(-1), "-Inf"},
		{math.NaN(), "NaN"},
	} {
		if got := formatValue(c.v); got != c.want {
			t.Errorf("formatValue(%v) = %q, want %q", c.v, got, c.want)
		}
	}
}

func TestNames(t *testing.T) {
	for _, c := range []struct {
		s             string
		metric, label bool
	}{
		{"a", true, true},
		{"_", true, true},
		{"A_b9", true, true},
		{"a:b", true, false},
		{":a", true, false},
		{"__x", true, true}, // syntactically valid; __ is refused separately for labels
		{"", false, false},
		{"9a", false, false},
		{"a-b", false, false},
		{"a b", false, false},
		{"é", false, false},
		{"a\n", false, false},
	} {
		if got := validMetricName(c.s); got != c.metric {
			t.Errorf("validMetricName(%q) = %v", c.s, got)
		}
		if got := validLabelName(c.s); got != c.label {
			t.Errorf("validLabelName(%q) = %v", c.s, got)
		}
	}
}

func TestInvalidIsSkippedAndReported(t *testing.T) {
	for _, c := range []struct {
		name   string
		write  func(*Writer)
		reason string
	}{
		{"metric name", func(w *Writer) { w.Gauge("bad-name", "h", S(1)) }, `skipped gauge "bad-name": invalid metric name`},
		{"empty metric name", func(w *Writer) { w.Counter("", "h", S(1)) }, `skipped counter "": invalid metric name`},
		{"newline in name", func(w *Writer) { w.Gauge("a\nb 1", "h", S(1)) }, `skipped gauge "a\nb 1": invalid metric name`},
		{"label name", func(w *Writer) { w.Gauge("g", "h", S(1, L("a-b", "x"))) }, `invalid label name "a-b"`},
		{"reserved label", func(w *Writer) { w.Gauge("g", "h", S(1, L("__name__", "x"))) }, `reserved label name "__name__"`},
		{"label twice", func(w *Writer) { w.Gauge("g", "h", S(1, L("a", "1"), L("a", "2"))) }, `label a given twice`},
		{"not UTF-8", func(w *Writer) { w.Gauge("g", "h", S(1, L("a", "\xff"))) }, `not UTF-8: "\xff"`},
		{"duplicate series", func(w *Writer) { w.Gauge("g", "h", S(1, L("a", "1"), L("b", "2")), S(2, L("b", "2"), L("a", "1"))) }, `duplicate series {b="2",a="1"}`},
		{"duplicate unlabelled", func(w *Writer) { w.Gauge("g", "h", S(1), S(2)) }, `duplicate series {}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			var w Writer
			w.Gauge("before", "", S(1))
			c.write(&w)
			w.Gauge("after", "", S(2))
			out := render(t, &w)
			if w.Err() == nil || !strings.Contains(w.Err().Error(), c.reason) {
				t.Errorf("Err = %v, want it to mention %s", w.Err(), c.reason)
			}
			// Nothing of the bad call, and everything of the good ones.
			if !strings.Contains(out, "\nbefore 1\n") || !strings.Contains(out, "\nafter 2\n") {
				t.Errorf("good families lost:\n%s", out)
			}
			if strings.Contains(out, "TYPE g ") || strings.Contains(out, "TYPE bad") {
				t.Errorf("bad family written:\n%s", out)
			}
			if !strings.HasSuffix(out, "# endpoint: "+strings.TrimPrefix(w.Err().Error(), "endpoint: ")+"\n") {
				t.Errorf("no comment naming the problem:\n%s", out)
			}
		})
	}
}

func TestAtomicCall(t *testing.T) {
	// One bad sample drops the whole call, never part of it.
	var w Writer
	w.Gauge("g", "h", S(1, L("a", "1")), S(2, L("-", "2")))
	if out := render(t, &w); strings.Contains(out, "g{") || w.Err() == nil {
		t.Errorf("partial call written:\n%s", out)
	}
}

func TestMergeSameType(t *testing.T) {
	var w Writer
	w.Gauge("g", "", S(1, L("from", "a")))
	w.Counter("c", "cnt", S(5))
	w.Gauge("g", "first help", S(2, L("from", "b")))
	w.Gauge("g", "ignored help", S(3, L("from", "c")))
	if w.Err() != nil {
		t.Fatalf("Err = %v", w.Err())
	}
	want := `# HELP g first help
# TYPE g gauge
g{from="a"} 1
g{from="b"} 2
g{from="c"} 3
# HELP c cnt
# TYPE c counter
c 5
`
	if got := render(t, &w); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func TestMergeDuplicateSeriesAcrossCalls(t *testing.T) {
	var w Writer
	w.Gauge("g", "", S(1, L("a", "1")))
	w.Gauge("g", "", S(2, L("b", "1")), S(9, L("a", "1")))
	out := render(t, &w)
	if w.Err() == nil || strings.Contains(out, "g{b=") || strings.Contains(out, " 9\n") {
		t.Errorf("duplicate across calls not refused: %v\n%s", w.Err(), out)
	}
}

func TestMergeDifferentType(t *testing.T) {
	var w Writer
	w.Counter("x", "", S(1))
	w.Gauge("x", "", S(2))
	w.Counter("x", "", S(3, L("k", "v")))
	want := `# TYPE x counter
x 1
x{k="v"} 3
# endpoint: skipped gauge "x": already written as a counter
`
	if got := render(t, &w); got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if w.Err().Error() != `endpoint: skipped gauge "x": already written as a counter` {
		t.Errorf("Err = %v", w.Err())
	}
}

func TestEmptyFamilyDeclaresButIsNotWritten(t *testing.T) {
	var w Writer
	w.Counter("c", "h")
	w.Gauge("c", "h", S(1))
	w.Gauge("g", "h")
	if got := render(t, &w); got != "# endpoint: skipped gauge \"c\": already written as a counter\n" {
		t.Errorf("got %q", got)
	}
}

func TestHelpNotUTF8IsRepaired(t *testing.T) {
	var w Writer
	w.Gauge("g", "a\xffb", S(1))
	if got := render(t, &w); !strings.HasPrefix(got, "# HELP g a\uFFFDb\n") || w.Err() != nil {
		t.Errorf("got %q, %v", got, w.Err())
	}
}

func TestUnicodeIsKept(t *testing.T) {
	var w Writer
	w.Gauge("g", "température — °C", S(21.5, L("salle", "Amphi Poincaré")))
	want := "# HELP g température — °C\n# TYPE g gauge\ng{salle=\"Amphi Poincaré\"} 21.5\n"
	if got := render(t, &w); got != want {
		t.Errorf("got %q", got)
	}
}

func TestCallerSliceIsCopied(t *testing.T) {
	var w Writer
	labels := []Label{L("a", "1")}
	w.Gauge("g", "", Sample{Labels: labels, Value: 1})
	labels[0] = L("a", "changed")
	if got := render(t, &w); !strings.Contains(got, `g{a="1"} 1`) {
		t.Errorf("caller mutation leaked:\n%s", got)
	}
}

type failWriter struct{}

func (failWriter) Write([]byte) (int, error) { return 0, errors.New("boom") }

func TestWriteToError(t *testing.T) {
	var w Writer
	w.Gauge("g", "", S(1))
	if _, err := w.WriteTo(failWriter{}); err == nil {
		t.Error("WriteTo swallowed the error")
	}
}
