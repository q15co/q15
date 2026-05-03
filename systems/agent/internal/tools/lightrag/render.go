package lightrag

import (
	"encoding/json"
	"fmt"
	"strings"
)

type queryResponse struct {
	Response   string      `json:"response"`
	References []reference `json:"references"`
}

type reference struct {
	ReferenceID string   `json:"reference_id"`
	FilePath    string   `json:"file_path"`
	Content     []string `json:"content"`
}

type ingestResponse struct {
	Status  string `json:"status"`
	Message string `json:"message"`
	TrackID string `json:"track_id"`
}

func renderQueryResponse(body []byte) string {
	var parsed queryResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "LightRAG Query Response:\n" + prettyJSON(body)
	}

	response := strings.TrimSpace(parsed.Response)
	if response == "" {
		response = "No response returned by LightRAG."
	}

	lines := []string{"Response:", response}
	if len(parsed.References) > 0 {
		lines = append(lines, "", "References:")
		for _, ref := range parsed.References {
			label := strings.TrimSpace(ref.ReferenceID)
			filePath := strings.TrimSpace(ref.FilePath)
			switch {
			case label != "" && filePath != "":
				lines = append(lines, fmt.Sprintf("- [%s] %s", label, filePath))
			case filePath != "":
				lines = append(lines, "- "+filePath)
			case label != "":
				lines = append(lines, "- ["+label+"]")
			}
		}
	}
	return strings.Join(lines, "\n")
}

func renderIngestResponse(source string, body []byte) string {
	var parsed ingestResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "LightRAG Ingest Response:\n" + prettyJSON(body)
	}

	var lines []string
	if source = strings.TrimSpace(source); source != "" {
		lines = append(lines, "Source: "+source)
	}
	if status := strings.TrimSpace(parsed.Status); status != "" {
		lines = append(lines, "Status: "+status)
	}
	if trackID := strings.TrimSpace(parsed.TrackID); trackID != "" {
		lines = append(lines, "Track-ID: "+trackID)
	}
	if message := strings.TrimSpace(parsed.Message); message != "" {
		lines = append(lines, "Message: "+message)
	}
	if len(lines) == 0 {
		return "LightRAG Ingest Response:\n" + prettyJSON(body)
	}
	return strings.Join(lines, "\n")
}

func renderGraphResponse(query string, body []byte) string {
	var labels []string
	if err := json.Unmarshal(body, &labels); err != nil {
		return "LightRAG Graph Response:\n" + prettyJSON(body)
	}

	query = strings.TrimSpace(query)
	if len(labels) == 0 {
		return fmt.Sprintf("No graph labels matched: %s", query)
	}

	lines := []string{fmt.Sprintf("Graph labels matching %q:", query)}
	for _, label := range labels {
		label = strings.TrimSpace(label)
		if label == "" {
			continue
		}
		lines = append(lines, "- "+label)
	}
	if len(lines) == 1 {
		return fmt.Sprintf("No graph labels matched: %s", query)
	}
	return strings.Join(lines, "\n")
}
