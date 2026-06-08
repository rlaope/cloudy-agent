package cli

import (
	"bytes"
	"context"
	"io"
	"testing"
)

func BenchmarkGatewayStatusJSON(b *testing.B) {
	b.Setenv("CLOUDY_HOME", b.TempDir())
	var out bytes.Buffer
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		out.Reset()
		if err := (gatewayCmd{}).Run(context.Background(), []string{"status", "--json"}, &out, io.Discard); err != nil {
			b.Fatal(err)
		}
	}
}
