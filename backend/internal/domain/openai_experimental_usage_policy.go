package domain

const (
	// OpenAINormalInputCacheTransferModePercent moves a percentage of ordinary
	// input tokens into the cache-write bucket.
	OpenAINormalInputCacheTransferModePercent = "percent"
	// OpenAINormalInputCacheTransferModeExact moves every ordinary input token
	// above KeepTokens into the cache-write bucket.
	OpenAINormalInputCacheTransferModeExact = "exact"
)

// OpenAINormalInputCacheRule moves ordinary input tokens into the cache-write
// bucket when its probability range is selected. An empty TransferMode keeps
// the original "percent" semantics for rules stored before modes existed.
type OpenAINormalInputCacheRule struct {
	TransferMode       string `json:"transfer_mode,omitempty"`
	TransferPercent    int    `json:"transfer_percent"`
	KeepTokens         int    `json:"keep_tokens,omitempty"`
	ProbabilityPercent int    `json:"probability_percent"`
}

// NormalizedTransferMode resolves the legacy empty mode to "percent".
func (r OpenAINormalInputCacheRule) NormalizedTransferMode() string {
	if r.TransferMode == "" {
		return OpenAINormalInputCacheTransferModePercent
	}
	return r.TransferMode
}
