package promcheck

import (
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-net-health/endpoint"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/model/textparse"
)

// Values chosen to exercise every escape and every number form.
var (
	tricky = []string{
		`back\slash`, `dq"uote`, "new\nline", `\n literal`, `\"`, `\\"`, "",
		"Amphi Poincaré — 21 °C", "tab\tand\rcr", "{}=,# ", "日本語",
	}
	values = []float64{
		0, math.Copysign(0, -1), 1, -1, 42, 1e6, 1 << 53, 1<<53 + 2, 1e21,
		0.5, 1e-7, math.MaxFloat64, math.SmallestNonzeroFloat64,
		math.Inf(1), math.Inf(-1), math.NaN(),
	}
	helpText = "Line one.\nLine two with a \\ backslash and \"quotes\"."
)

func torture(w *endpoint.Writer) {
	var ss []endpoint.Sample
	for i, v := range tricky {
		ss = append(ss, endpoint.S(float64(i), endpoint.L("v", v), endpoint.L("i", string(rune('a'+i)))))
	}
	w.Gauge("tricky_labels", helpText, ss...)
	ss = nil
	for i, v := range values {
		ss = append(ss, endpoint.S(v, endpoint.L("i", string(rune('a'+i)))))
	}
	w.Gauge("tricky_values", "", ss...)
	w.Counter("merged_total", "From two calls.", endpoint.S(1, endpoint.L("from", "a")))
	w.Counter("merged_total", "", endpoint.S(2, endpoint.L("from", "b")))
	// Everything below is refused by the Writer; the scrape must still parse.
	w.Gauge("merged_total", "", endpoint.S(3))
	w.Gauge("bad-name", "", endpoint.S(1))
	w.Gauge("bad\nname 1\n# TYPE bad gauge", "", endpoint.S(1))
	w.Gauge("bad_label", "", endpoint.S(1, endpoint.L("__reserved", "x")))
	w.Gauge("bad_value", "", endpoint.S(1, endpoint.L("v", "\xff\xfe")))
	w.Gauge("dup", "", endpoint.S(1), endpoint.S(2))
}

func scrape(t *testing.T) (body []byte, contentType string) {
	t.Helper()
	srv := httptest.NewServer(endpoint.Handler(endpoint.Options{Collectors: []endpoint.Collector{
		endpoint.GoRuntime, endpoint.BuildInfo("promcheck"), torture,
	}}))
	defer srv.Close()
	res, err := http.Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, err = io.ReadAll(res.Body)
	if err != nil || res.StatusCode != 200 {
		t.Fatalf("scrape: %d %v", res.StatusCode, err)
	}
	return body, res.Header.Get("Content-Type")
}

func sameFloat(a, b float64) bool {
	return math.Float64bits(a) == math.Float64bits(b) || (math.IsNaN(a) && math.IsNaN(b))
}

// TestPrometheusScraper parses a scrape the way a Prometheus server does:
// the parser is chosen from the Content-Type, with no fallback, so an
// unrecognised Content-Type fails here as it would fail the scrape.
func TestPrometheusScraper(t *testing.T) {
	body, ct := scrape(t)
	p, err := textparse.New(body, ct, labels.NewSymbolTable(), textparse.ParserOptions{})
	if err != nil || p == nil {
		t.Fatalf("Content-Type %q not accepted: %v", ct, err)
	}
	got := map[string]float64{}
	help := map[string]string{}
	types := map[string]model.MetricType{}
	for {
		e, err := p.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("parse: %v\n%s", err, body)
		}
		switch e {
		case textparse.EntryHelp:
			n, h := p.Help()
			help[string(n)] = string(h)
		case textparse.EntryType:
			n, typ := p.Type()
			if _, dup := types[string(n)]; dup {
				t.Errorf("second TYPE for %s", n)
			}
			types[string(n)] = typ
		case textparse.EntrySeries:
			_, _, v := p.Series()
			var ls labels.Labels
			p.Labels(&ls)
			got[ls.String()] = v
		}
	}
	for i, v := range tricky {
		key := labels.FromStrings("__name__", "tricky_labels", "v", v, "i", string(rune('a'+i))).String()
		if gv, ok := got[key]; !ok || gv != float64(i) {
			t.Errorf("label value %q did not round-trip (series %s)", v, key)
		}
	}
	for i, v := range values {
		key := labels.FromStrings("__name__", "tricky_values", "i", string(rune('a'+i))).String()
		if gv, ok := got[key]; !ok || !sameFloat(gv, v) {
			t.Errorf("value %v read back as %v (present %v)", v, gv, ok)
		}
	}
	// Help() returns the help text unescaped.
	if help["tricky_labels"] != helpText {
		t.Errorf("help = %q", help["tricky_labels"])
	}
	wantTypes := map[string]model.MetricType{
		"go_goroutines": "gauge", "go_memstats_heap_alloc_bytes": "gauge", "go_memstats_sys_bytes": "gauge",
		"go_gc_cycles_total": "counter", "promcheck_build_info": "gauge",
		"tricky_labels": "gauge", "tricky_values": "gauge", "merged_total": "counter",
	}
	if len(types) != len(wantTypes) {
		t.Errorf("families %v, want %v", types, wantTypes)
	}
	for n, typ := range wantTypes {
		if types[n] != typ {
			t.Errorf("TYPE %s = %q, want %q", n, types[n], typ)
		}
	}
	if len(got) != len(tricky)+len(values)+4+1+2 {
		t.Errorf("%d series, want %d", len(got), len(tricky)+len(values)+7)
	}
}

// TestExpfmt parses the same scrape with the client library's parser, in
// the legacy (0.0.4) name validation scheme, which unlike textparse also
// refuses a second TYPE line.
func TestExpfmt(t *testing.T) {
	body, _ := scrape(t)
	p := expfmt.NewTextParser(model.LegacyValidation)
	fams, err := p.TextToMetricFamilies(strings.NewReader(string(body)))
	if err != nil {
		t.Fatalf("parse: %v\n%s", err, body)
	}
	tl := fams["tricky_labels"]
	if tl.GetHelp() != helpText {
		t.Errorf("help = %q, want %q", tl.GetHelp(), helpText)
	}
	byI := func(m *dto.Metric) (i, v string) {
		for _, lp := range m.GetLabel() {
			switch lp.GetName() {
			case "i":
				i = lp.GetValue()
			case "v":
				v = lp.GetValue()
			}
		}
		return
	}
	if len(tl.GetMetric()) != len(tricky) {
		t.Fatalf("%d tricky_labels samples", len(tl.GetMetric()))
	}
	for _, m := range tl.GetMetric() {
		i, v := byI(m)
		if want := tricky[i[0]-'a']; v != want {
			t.Errorf("label %s: %q, want %q", i, v, want)
		}
	}
	for _, m := range fams["tricky_values"].GetMetric() {
		i, _ := byI(m)
		if got, want := m.GetGauge().GetValue(), values[i[0]-'a']; !sameFloat(got, want) {
			t.Errorf("value %s: %v, want %v", i, got, want)
		}
	}
	if n := len(fams["merged_total"].GetMetric()); n != 2 || fams["merged_total"].GetType() != dto.MetricType_COUNTER {
		t.Errorf("merged_total: %d samples, %v", n, fams["merged_total"].GetType())
	}
	for _, bad := range []string{"bad-name", "bad_label", "bad_value", "dup"} {
		if _, ok := fams[bad]; ok {
			t.Errorf("%s was written", bad)
		}
	}
	bi := fams["promcheck_build_info"].GetMetric()
	if len(bi) != 1 || len(bi[0].GetLabel()) != 3 || bi[0].GetGauge().GetValue() != 1 {
		t.Errorf("build info: %v", bi)
	}
}

// TestParsersCanFail is the positive control: each parser must reject the
// mistakes the Writer exists to prevent, or passing the tests above proves
// nothing.
//
// Neither parser refuses a duplicate series (expfmt accepts "x 1\nx 2\n";
// a Prometheus server drops the second sample at ingestion and counts it in
// prometheus_target_scrapes_sample_duplicate_timestamp_total), and both
// accept any spelling strconv.ParseFloat does, "inf" included: the Writer's
// own checks, tested in the endpoint module, are the only guard there.
func TestParsersCanFail(t *testing.T) {
	for _, bad := range []string{
		"# TYPE x gauge\nx{a=\"\"\"} 1\n",   // unescaped quote
		"# TYPE x gauge\nx{a-b=\"1\"} 1\n",  // invalid label name
		"# TYPE x gauge\nx 1 2 3\n",         // garbage after the value
		"# TYPE x gauge\nx{a=\"\xff\"} 1\n", // invalid UTF-8
	} {
		if _, err := parseExpfmt(bad); err == nil {
			t.Errorf("expfmt accepted %q", bad)
		}
	}
	for _, bad := range []string{
		"# TYPE x gauge\n# TYPE x counter\nx 1\n", // second TYPE
		"# TYPE bad-name gauge\n",                 // invalid metric name
	} {
		if _, err := parseExpfmt(bad); err == nil {
			t.Errorf("expfmt accepted %q", bad)
		}
	}
	for _, bad := range []string{
		"# TYPE x gauge\nx{a=\"\"\"} 1\n",
		"# TYPE x gauge\nx 1 2 3\n",
		"# TYPE x gauge\nx{a-b=\"1\"} 1\n",
	} {
		p, err := textparse.New([]byte(bad), "text/plain; version=0.0.4; charset=utf-8", labels.NewSymbolTable(), textparse.ParserOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for err == nil {
			_, err = p.Next()
		}
		if errors.Is(err, io.EOF) {
			t.Errorf("textparse accepted %q", bad)
		}
	}
	// And an unrecognised Content-Type fails the scrape.
	if p, err := textparse.New([]byte("x 1\n"), "application/x-nonsense", labels.NewSymbolTable(), textparse.ParserOptions{}); err == nil && p != nil {
		t.Error("textparse accepted an unknown Content-Type")
	}
}

func parseExpfmt(s string) (map[string]*dto.MetricFamily, error) {
	p := expfmt.NewTextParser(model.LegacyValidation)
	return p.TextToMetricFamilies(strings.NewReader(s))
}
