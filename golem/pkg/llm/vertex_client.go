package llm

import (
	"context"
	"fmt"

	aiplatform "cloud.google.com/go/aiplatform/apiv1beta1"
	aiplatformpb "cloud.google.com/go/aiplatform/apiv1beta1/aiplatformpb"
	"google.golang.org/api/option"
)

// VertexClient provides methods to interact with Vertex AI models.
type VertexClient struct {
	projectID string
	location  string
	modelID   string // e.g., "gemini-pro"
	client    *aiplatform.PredictionClient // Use PredictionClient for GenerateContent
}

// NewVertexClient creates a new VertexClient configured for Gemini models.
func NewVertexClient(projectID, location, modelID string) (*VertexClient, error) {
	ctx := context.Background()
	client, err := aiplatform.NewPredictionClient(ctx, option.WithEndpoint(fmt.Sprintf("%s-aiplatform.googleapis.com:443", location)))
	if err != nil {
		return nil, fmt.Errorf("failed to create prediction client: %v", err)
	}

	return &VertexClient{
		projectID: projectID,
		location:  location,
		modelID:   modelID,
		client:    client,
	}, nil
}

// Predict sends a prediction request to the Vertex AI Gemini model.
func (vc *VertexClient) Predict(ctx context.Context, prompt string) (string, error) {
	// The model name needs to be fully qualified for the prediction client.
	modelPath := fmt.Sprintf("projects/%s/locations/%s/publishers/google/models/%s", vc.projectID, vc.location, vc.modelID)

	// Construct the content with a Text part and assign the "user" role.
	content := []*aiplatformpb.Content{{
		Role: "user", // Assign the role here
		Parts: []*aiplatformpb.Part{{
			Data: &aiplatformpb.Part_Text{Text: prompt}, // Correct way to set text for Part
		}},
	}}

	req := &aiplatformpb.GenerateContentRequest{
		Model:    modelPath,
		Contents: content,
	}

	resp, err := vc.client.GenerateContent(ctx, req)
	if err != nil {
		return "", fmt.Errorf("failed to generate content: %v", err)
	}

	// Assuming a single text response part
	if len(resp.Candidates) == 0 || len(resp.Candidates[0].GetContent().GetParts()) == 0 {
		return "", fmt.Errorf("no response content found")
	}

	// Extract the text from the response part.
	var fullResponse string
	for _, part := range resp.Candidates[0].GetContent().GetParts() {
		if text := part.GetText(); text != "" {
			fullResponse += text
		}
	}

	return fullResponse, nil
}

// Close closes the prediction client.
func (vc *VertexClient) Close() error {
	return vc.client.Close()
}
