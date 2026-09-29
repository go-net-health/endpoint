// Package endpoint is the served side of health checking: an http.Handler
// answering /healthz, /readyz and /metrics, the last in the Prometheus text
// exposition format 0.0.4.
//
// It depends on the standard library only — no prometheus/client_golang —
// so a size-conscious binary can expose metrics without importing a
// metrics framework. There are no registries and no metric objects: a
// Collector is a function called on every scrape, which writes the current
// values of its families to a Writer.
//
//	http.Handle("/", endpoint.Handler(endpoint.Options{
//		Ready:      func() error { return db.Ping() },
//		Collectors: []endpoint.Collector{endpoint.GoRuntime, endpoint.BuildInfo("myapp")},
//	}))
//
// # What a Writer guarantees
//
// Whatever a Collector writes, a scrape stays parseable. Names are checked
// against the Prometheus data model — metric names must match
// [a-zA-Z_:][a-zA-Z0-9_:]*, label names [a-zA-Z_][a-zA-Z0-9_]* and must not
// start with "__" (reserved for Prometheus itself). A call to Counter or
// Gauge is atomic: if its name, any label name, any label value (which must
// be valid UTF-8), or any label set (duplicated within a sample, or already
// written for that family) is wrong, none of its samples are written. The
// call is recorded instead: Writer.Err reports the first such problem, and
// the scrape ends with one "# endpoint: skipped ..." comment line per
// problem, which Prometheus ignores and a person reading the scrape sees.
//
// Writing a family name twice in one scrape — typically from two
// collectors — merges the samples under a single # HELP and # TYPE if both
// calls use the same type; the first non-empty help text wins. A second
// call with the other type is a problem as above, and the first type stands.
//
// Values are not checked: a Counter may be given a negative or decreasing
// value, and Prometheus will treat it as a reset. Names are not rewritten
// either: the convention that counter names end in "_total" is the caller's
// to follow — renaming a metric behind its author's back would break every
// query written against the name they chose.
//
// # Number formatting
//
// +Inf, -Inf and NaN are written as such. A value that is an integer of
// magnitude at most 2^53 is written in plain decimal (1234567, not
// 1.234567e+06); every other value in the shortest form that parses back to
// the same float64 (strconv 'g', -1). Both forms are accepted by Go's
// strconv.ParseFloat, which is what the Prometheus parsers use; the plain
// form is chosen because byte and object counts are integers and are easier
// to read and grep in full, and 2^53 is where float64 stops representing
// every integer exactly.
package endpoint

import (
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

// A Label is one name="value" pair of a sample.
type Label struct{ Name, Value string }

// L returns the Label name="value".
func L(name, value string) Label { return Label{Name: name, Value: value} }

// A Sample is one value of a family, identified by its labels.
type Sample struct {
	Labels []Label
	Value  float64
}

// S returns a Sample of value v with the given labels.
func S(v float64, labels ...Label) Sample { return Sample{Labels: labels, Value: v} }

// A Writer writes one scrape in text format 0.0.4.
//
// The zero value is ready to use. Families are buffered and written, in the
// order their names were first seen, by WriteTo. A Writer is not safe for
// concurrent use; the Handler gives every scrape its own.
type Writer struct {
	families []*family
	byName   map[string]*family
	problems []string
}

type family struct {
	name, help, typ string
	samples         []Sample
	series          map[string]bool
}

// Counter writes a counter family. See the package documentation for what
// happens when a name or label is invalid or the family already exists.
func (w *Writer) Counter(name, help string, samples ...Sample) {
	w.add("counter", name, help, samples)
}

// Gauge writes a gauge family. See the package documentation for what
// happens when a name or label is invalid or the family already exists.
func (w *Writer) Gauge(name, help string, samples ...Sample) {
	w.add("gauge", name, help, samples)
}

// Err returns the first problem recorded by Counter or Gauge, or nil. Every
// problem also appears as a comment line at the end of the scrape.
func (w *Writer) Err() error {
	if len(w.problems) == 0 {
		return nil
	}
	return errors.New("endpoint: " + w.problems[0])
}

func (w *Writer) add(typ, name, help string, samples []Sample) {
	if !validMetricName(name) {
		w.skip(typ, name, "invalid metric name")
		return
	}
	f := w.byName[name]
	if f != nil && f.typ != typ {
		w.skip(typ, name, "already written as a "+f.typ)
		return
	}
	keys := make([]string, len(samples))
	seen := make(map[string]bool, len(samples))
	for i, s := range samples {
		key, reason := seriesKey(s.Labels)
		switch {
		case reason != "":
		case seen[key] || (f != nil && f.series[key]):
			reason = "duplicate series " + labelString(s.Labels)
		}
		if reason != "" {
			w.skip(typ, name, reason)
			return
		}
		seen[key] = true
		keys[i] = key
	}
	if f == nil {
		f = &family{name: name, typ: typ, series: seen}
		if w.byName == nil {
			w.byName = map[string]*family{}
		}
		w.byName[name] = f
		w.families = append(w.families, f)
	} else {
		for _, k := range keys {
			f.series[k] = true
		}
	}
	if f.help == "" {
		f.help = strings.ToValidUTF8(help, "�")
	}
	for _, s := range samples {
		// Copy the labels: the caller may reuse its slice before WriteTo.
		f.samples = append(f.samples, Sample{Labels: append([]Label(nil), s.Labels...), Value: s.Value})
	}
}

func (w *Writer) skip(typ, name, reason string) {
	w.problems = append(w.problems, fmt.Sprintf("skipped %s %q: %s", typ, name, reason))
}

// seriesKey returns a key identifying the label set regardless of label
// order, or the reason the labels are invalid.
func seriesKey(labels []Label) (key, reason string) {
	pairs := make([]string, len(labels))
	names := make(map[string]bool, len(labels))
	for i, l := range labels {
		switch {
		case !validLabelName(l.Name):
			return "", fmt.Sprintf("invalid label name %q", l.Name)
		case strings.HasPrefix(l.Name, "__"):
			return "", fmt.Sprintf("reserved label name %q", l.Name)
		case !utf8.ValidString(l.Value):
			return "", fmt.Sprintf("label %s has a value that is not UTF-8: %q", l.Name, l.Value)
		case names[l.Name]:
			return "", fmt.Sprintf("label %s given twice", l.Name)
		}
		names[l.Name] = true
		// Names are ASCII and values valid UTF-8, so 0xff cannot occur in
		// either and separates them unambiguously.
		pairs[i] = l.Name + "\xff" + l.Value
	}
	sort.Strings(pairs)
	return strings.Join(pairs, "\xff\xff"), ""
}

func labelString(labels []Label) string {
	var b strings.Builder
	writeLabels(&b, labels)
	if b.Len() == 0 {
		return "{}"
	}
	return b.String()
}

// WriteTo writes the scrape to dst.
func (w *Writer) WriteTo(dst io.Writer) (int64, error) {
	var b strings.Builder
	for _, f := range w.families {
		if len(f.samples) == 0 {
			continue
		}
		if f.help != "" {
			b.WriteString("# HELP ")
			b.WriteString(f.name)
			b.WriteByte(' ')
			helpEscaper.WriteString(&b, f.help)
			b.WriteByte('\n')
		}
		b.WriteString("# TYPE ")
		b.WriteString(f.name)
		b.WriteByte(' ')
		b.WriteString(f.typ)
		b.WriteByte('\n')
		for _, s := range f.samples {
			b.WriteString(f.name)
			writeLabels(&b, s.Labels)
			b.WriteByte(' ')
			b.WriteString(formatValue(s.Value))
			b.WriteByte('\n')
		}
	}
	for _, p := range w.problems {
		b.WriteString("# endpoint: ")
		b.WriteString(p)
		b.WriteByte('\n')
	}
	n, err := io.WriteString(dst, b.String())
	return int64(n), err
}

var (
	// Text format 0.0.4: in HELP, backslash and line feed are escaped; in
	// label values, backslash, double quote and line feed.
	helpEscaper  = strings.NewReplacer(`\`, `\\`, "\n", `\n`)
	valueEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)
)

func writeLabels(b *strings.Builder, labels []Label) {
	if len(labels) == 0 {
		return
	}
	b.WriteByte('{')
	for i, l := range labels {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(l.Name)
		b.WriteString(`="`)
		valueEscaper.WriteString(b, l.Value)
		b.WriteByte('"')
	}
	b.WriteByte('}')
}

func formatValue(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	case v == math.Trunc(v) && math.Abs(v) <= 1<<53:
		return strconv.FormatFloat(v, 'f', -1, 64)
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func validMetricName(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(isLetter(c) || c == '_' || c == ':' || (i > 0 && isDigit(c))) {
			return false
		}
	}
	return s != ""
}

func validLabelName(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !(isLetter(c) || c == '_' || (i > 0 && isDigit(c))) {
			return false
		}
	}
	return s != ""
}

func isLetter(c byte) bool { return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' }
func isDigit(c byte) bool  { return '0' <= c && c <= '9' }
