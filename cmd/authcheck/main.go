package main

import (
	"context"
	"crypto/sha256"
	"flag"
	"fmt"
	"log"
	"time"

	"antigravity-go-proxy/internal/auth"
)

func main() {
	timeout := flag.Duration("timeout", 30*time.Second, "OAuth request timeout")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	credentials, err := (auth.Manager{}).Get(ctx)
	if err != nil {
		log.Fatal(err)
	}
	digest := sha256.Sum256([]byte(credentials.AccessToken))
	fmt.Printf("access_token=<redacted sha256=%x length=%d> refreshed=%t expiry=%s email=%s\n",
		digest[:6], len(credentials.AccessToken), credentials.Refreshed,
		credentials.Expiry.UTC().Format(time.RFC3339), credentials.Email)
}
