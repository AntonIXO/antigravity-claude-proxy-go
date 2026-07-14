package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"time"

	"antigravity-go-proxy/internal/auth"
	"antigravity-go-proxy/internal/cloudcode"
)

func main() {
	operation := flag.String("operation", "models", "gate operation: models or load")
	project := flag.String("project", "", "optional Cloud Code project ID")
	timeout := flag.Duration("timeout", 30*time.Second, "request timeout")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	credentials, err := (auth.Manager{}).Get(ctx)
	if err != nil {
		log.Fatal(err)
	}
	client := cloudcode.New(cloudcode.Options{AccessToken: credentials.AccessToken, Timeout: *timeout})
	defer client.CloseIdleConnections()

	var response cloudcode.Response
	switch *operation {
	case "models":
		response, err = client.FetchAvailableModels(ctx, *project)
	case "load":
		response, err = client.LoadCodeAssist(ctx, *project)
	default:
		log.Fatalf("unknown operation %q", *operation)
	}
	if err != nil {
		log.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(response.Body, &document); err != nil {
		log.Fatalf("decode successful Cloud Code response: %v", err)
	}
	fmt.Printf("cloudcode_ok operation=%s endpoint=%s status=%d bytes=%d top_level_fields=%d\n",
		*operation, response.Endpoint, response.StatusCode, len(response.Body), len(document))
}
