package dslconfig

// ProviderObservability contains provider-scoped observation rules.
type ProviderObservability struct {
	UpstreamRequestID     *UpstreamRequestIDRule
	UpstreamRequestIDJSON *UpstreamRequestIDJSONRule
}

// UpstreamRequestIDRule lists upstream response headers in lookup priority order.
type UpstreamRequestIDRule struct {
	Headers []string
}

// UpstreamRequestIDJSONRule selects an upstream request ID from a non-stream
// response body after response mapping and before response JSON operations.
type UpstreamRequestIDJSONRule struct {
	Path string
}
