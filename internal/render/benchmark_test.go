package render

import (
	"fmt"
	"strings"
	"testing"
)

var benchmarkMarkdown = strings.Repeat(`# Incident summary

The **checkout-api** deployment has elevated p99 latency and intermittent
5xx errors. The most likely cause is a recent rollout that increased database
connection pressure.

## Evidence

- checkout-api-7df9c: restart count stayed flat.
- postgres-primary: active connections rose sharply.
- prometheus: p99 latency crossed the SLO burn threshold.

~~~log
2026-06-08T00:00:00Z level=warn msg="slow query" duration=812ms
~~~

Recommended next step: inspect the rollout and database pool settings.

`, 20)

func BenchmarkRenderMarkdownColor(b *testing.B) {
	theme := NewTheme(false)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		out, err := RenderMarkdown(benchmarkMarkdown, theme, 100)
		if err != nil {
			b.Fatal(err)
		}
		if len(out) == 0 {
			b.Fatal("empty markdown output")
		}
	}
}

func BenchmarkRenderMarkdownNoColor(b *testing.B) {
	theme := NewTheme(true)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		out, err := RenderMarkdown(benchmarkMarkdown, theme, 100)
		if err != nil {
			b.Fatal(err)
		}
		if len(out) == 0 {
			b.Fatal("empty markdown output")
		}
	}
}

func BenchmarkTableRenderLargeNoColor(b *testing.B) {
	tbl := benchmarkTable(250)
	theme := NewTheme(true)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		out := tbl.Render(120, theme)
		if len(out) == 0 {
			b.Fatal("empty table output")
		}
	}
}

func benchmarkTable(rows int) Table {
	tbl := Table{
		Headers: []string{"NAMESPACE", "POD", "STATUS", "RESTARTS", "AGE", "MESSAGE"},
		Aligns:  []Align{AlignLeft, AlignLeft, AlignLeft, AlignRight, AlignRight, AlignLeft},
		Rows:    make([][]string, 0, rows),
	}
	for i := 0; i < rows; i++ {
		tbl.Rows = append(tbl.Rows, []string{
			"production",
			fmt.Sprintf("checkout-api-%03d-7df9c", i),
			"Running",
			fmt.Sprintf("%d", i%4),
			fmt.Sprintf("%dh", 1+i%72),
			"readiness probe healthy; latency above historical baseline",
		})
	}
	return tbl
}
