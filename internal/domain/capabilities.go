package domain

// CapabilityFact describes evidence, never permission to execute an account.
type CapabilityFact struct {
	Status string `json:"status"`
	Code   string `json:"code"`
}

type PTYCapability struct {
	Supported     bool           `json:"supported"`
	Available     bool           `json:"available"`
	Code          string         `json:"code"`
	Qualification CapabilityFact `json:"qualification"`
}

type CLICapability struct {
	ID                string         `json:"id"`
	CLI               string         `json:"cli"`
	Selected          bool           `json:"selected"`
	Supported         bool           `json:"supported"`
	Isolation         Isolation      `json:"isolation"`
	Installation      CapabilityFact `json:"installation"`
	Compatibility     CapabilityFact `json:"compatibility"`
	CompatibleBanners []string       `json:"compatible_banners"`
	AccountReadiness  CapabilityFact `json:"account_readiness"`
	Qualification     CapabilityFact `json:"qualification"`
}
