package sandbox

type HelperRequest struct {
	Settings Settings `json:"settings"`
	Command  string   `json:"command,omitempty"`
}

type HelperResponse struct {
	Output string `json:"output,omitempty"`
	Error  string `json:"error,omitempty"`
}
