package gpu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rlaope/cloudy/internal/tools/prom"
)

// ---- nvidia_smi tests ----

const nvidiaSMIFixture = `0, Tesla V100-SXM2-32GB, 92, 28000, 32510, 87, 300.00
1, Tesla V100-SXM2-32GB, 45, 16000, 32510, 72, 180.50
2, Tesla V100-SXM2-32GB, 10, 5000, 32510, 60, 120.00
`

func TestNvidiaSMI_ParsesOutput(t *testing.T) {
	restore := func() {}
	orig := smiRunner
	smiRunner = func(ctx context.Context, name string, args ...string) (string, string, error) {
		return nvidiaSMIFixture, "", nil
	}
	restore = func() { smiRunner = orig }
	defer restore()

	tool := NewNvidiaSMITool()

	if !tool.ReadOnly() {
		t.Fatal("ReadOnly must return true")
	}
	if tool.Name() != "gpu.nvidia_smi" {
		t.Fatalf("unexpected name: %s", tool.Name())
	}
	var schemaObj map[string]any
	if err := json.Unmarshal(tool.Schema(), &schemaObj); err != nil {
		t.Fatalf("Schema() not valid JSON: %v", err)
	}

	obs, err := tool.Run(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if obs.Table == nil {
		t.Fatal("expected Table in Observation")
	}
	if len(obs.Table.Rows) != 3 {
		t.Fatalf("expected 3 GPU rows, got %d", len(obs.Table.Rows))
	}
	// GPU 0: util=92 (>90 → warn), temp=87 (>85 → err).
	// GPU 1: mem=16000/32510 ≈ 49% (< 85 → no warn).
	// Just verify the data is there.
	if obs.Table.Rows[0][0] != "0" {
		t.Errorf("expected index=0, got %s", obs.Table.Rows[0][0])
	}
	if obs.Table.Rows[0][2] != "92" {
		t.Errorf("expected util=92, got %s", obs.Table.Rows[0][2])
	}
}

func TestNvidiaSMI_ColorizerNotNil(t *testing.T) {
	orig := smiRunner
	smiRunner = func(ctx context.Context, name string, args ...string) (string, string, error) {
		return nvidiaSMIFixture, "", nil
	}
	defer func() { smiRunner = orig }()

	tool := NewNvidiaSMITool()
	obs, err := tool.Run(context.Background(), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if obs.Table.Colorizer == nil {
		t.Fatal("expected Colorizer to be set for threshold colouring")
	}
}

// ---- dcgm tests ----

const dcgmUtilResponse = `{
  "status":"success",
  "data":{
    "resultType":"vector",
    "result":[
      {"metric":{"gpu":"0","instance":"node-1"},"value":[1700000000,"95"]},
      {"metric":{"gpu":"1","instance":"node-1"},"value":[1700000000,"40"]},
      {"metric":{"gpu":"0","instance":"node-2"},"value":[1700000000,"70"]}
    ]
  }
}`

const dcgmFBUsedResponse = `{
  "status":"success",
  "data":{
    "resultType":"vector",
    "result":[
      {"metric":{"gpu":"0","instance":"node-1"},"value":[1700000000,"28000"]},
      {"metric":{"gpu":"1","instance":"node-1"},"value":[1700000000,"16000"]},
      {"metric":{"gpu":"0","instance":"node-2"},"value":[1700000000,"5000"]}
    ]
  }
}`

const dcgmFBFreeResponse = `{
  "status":"success",
  "data":{
    "resultType":"vector",
    "result":[
      {"metric":{"gpu":"0","instance":"node-1"},"value":[1700000000,"4510"]},
      {"metric":{"gpu":"1","instance":"node-1"},"value":[1700000000,"16510"]},
      {"metric":{"gpu":"0","instance":"node-2"},"value":[1700000000,"27510"]}
    ]
  }
}`

const dcgmTempResponse = `{
  "status":"success",
  "data":{
    "resultType":"vector",
    "result":[
      {"metric":{"gpu":"0","instance":"node-1"},"value":[1700000000,"88"]},
      {"metric":{"gpu":"1","instance":"node-1"},"value":[1700000000,"72"]},
      {"metric":{"gpu":"0","instance":"node-2"},"value":[1700000000,"65"]}
    ]
  }
}`

func fakeDCGMServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/query", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query().Get("query")
		w.Header().Set("Content-Type", "application/json")
		switch q {
		case "DCGM_FI_DEV_GPU_UTIL":
			w.Write([]byte(dcgmUtilResponse))
		case "DCGM_FI_DEV_FB_USED":
			w.Write([]byte(dcgmFBUsedResponse))
		case "DCGM_FI_DEV_FB_FREE":
			w.Write([]byte(dcgmFBFreeResponse))
		case "DCGM_FI_DEV_GPU_TEMP":
			w.Write([]byte(dcgmTempResponse))
		default:
			http.Error(w, "unknown metric", 404)
		}
	})
	return httptest.NewServer(mux)
}

func TestDCGM_QueryAndSort(t *testing.T) {
	srv := fakeDCGMServer(t)
	defer srv.Close()

	c, err := prom.NewClient(srv.URL, "", "", "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	tool := NewDCGMTool(map[string]*prom.Client{"default": c})

	if !tool.ReadOnly() {
		t.Fatal("ReadOnly must return true")
	}
	if tool.Name() != "gpu.dcgm_metrics" {
		t.Fatalf("unexpected name: %s", tool.Name())
	}
	var schemaObj map[string]any
	if err := json.Unmarshal(tool.Schema(), &schemaObj); err != nil {
		t.Fatalf("Schema() not valid JSON: %v", err)
	}

	args, _ := json.Marshal(map[string]any{"top": 10})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if obs.Table == nil {
		t.Fatal("expected Table")
	}
	// 3 GPUs across 2 nodes.
	if len(obs.Table.Rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(obs.Table.Rows))
	}
	// Top entry should be node-1/gpu-0 with util=95.
	if obs.Table.Rows[0][2] != "95.0" {
		t.Errorf("expected highest util=95.0, got %s", obs.Table.Rows[0][2])
	}
}

func TestDCGM_TopCap(t *testing.T) {
	srv := fakeDCGMServer(t)
	defer srv.Close()

	c, _ := prom.NewClient(srv.URL, "", "", "")
	tool := NewDCGMTool(map[string]*prom.Client{"default": c})

	args, _ := json.Marshal(map[string]any{"top": 1})
	obs, err := tool.Run(context.Background(), args)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(obs.Table.Rows) != 1 {
		t.Fatalf("expected 1 row with top=1, got %d", len(obs.Table.Rows))
	}
}

func TestDCGM_ReadOnly(t *testing.T) {
	tool := NewDCGMTool(map[string]*prom.Client{})
	if !tool.ReadOnly() {
		t.Fatal("ReadOnly must return true")
	}
}
