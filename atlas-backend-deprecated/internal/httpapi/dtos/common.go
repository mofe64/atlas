package dtos

type HealthResponse struct {
	Service string `json:"service"`
	Status  string `json:"status"`
	Time    string `json:"time"`
}

type VersionResponse struct {
	Service string `json:"service"`
	Version string `json:"version"`
}
