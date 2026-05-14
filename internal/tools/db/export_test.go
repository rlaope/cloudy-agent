package db

// ParseK8sDSN exposes the internal parseK8sDSN function for white-box testing.
func ParseK8sDSN(dsn string) (ctxName, namespace, svcName string, port int, ok bool) {
	return parseK8sDSN(dsn)
}
