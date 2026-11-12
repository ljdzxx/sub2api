package domain

// OpenAINormalInputCacheRule moves a percentage of ordinary input tokens into
// the cache-write bucket when its probability range is selected.
type OpenAINormalInputCacheRule struct {
	TransferPercent    int `json:"transfer_percent"`
	ProbabilityPercent int `json:"probability_percent"`
}
