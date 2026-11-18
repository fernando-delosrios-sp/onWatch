package api

// ProviderModel represents a model discovered from a provider's models endpoint.
type ProviderModel struct {
	ID            string   `json:"id"`
	Name          string   `json:"name,omitempty"`
	Provider      string   `json:"provider"`
	OwnedBy       string   `json:"owned_by,omitempty"`
	Description   string   `json:"description,omitempty"`
	ContextWindow int      `json:"context_window,omitempty"`
	MaxOutput     int      `json:"max_output,omitempty"`
	Free          bool     `json:"free,omitempty"`
	Methods       []string `json:"supported_methods,omitempty"`
}
