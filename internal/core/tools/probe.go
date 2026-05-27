package tools

// GroupReport describes the registration outcome of a single tool group
// (e.g. "k8s", "db", "log"). Every group that participates in the inventory
// surface produces one report at wire time.
//
// A group is "registered" when at least one of its tools made it into the
// registry. A group is "skipped" when its probe (binary presence, endpoint
// reachability, capability check) failed; in that case Reason explains why
// so /tools can show "skipped because: kubeconfig not found" instead of
// silently hiding the group.
type GroupReport struct {
	Name    string   // group key, e.g. "k8s", "jvm", "db"
	Tools   []string // tool names that were registered (sorted alphabetically)
	Skipped bool     // true when the group's probe failed
	Reason  string   // human-readable explanation when Skipped
}

// Inventory is the union of all GroupReports for a Registry.
type Inventory struct {
	Groups []GroupReport // sorted by group name
}
