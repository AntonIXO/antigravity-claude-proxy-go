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
	refresh := flag.Bool("refresh", false, "force a refresh using credentials discovered from the installed agy executable")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	manager := auth.Manager{}
	var credentials auth.Credentials
	var err error
	if *refresh {
		path, pathErr := auth.DefaultTokenPath()
		if pathErr != nil {
			log.Fatal(pathErr)
		}
		token, readErr := auth.Read(path)
		if readErr != nil {
			log.Fatal(readErr)
		}
		credentials, err = manager.FromRefreshToken(ctx, token.RefreshToken, "")
	} else {
		credentials, err = manager.Get(ctx)
	}
	if err != nil {
		log.Fatal(err)
	}
	digest := sha256.Sum256([]byte(credentials.AccessToken))
	fmt.Printf("access_token=<redacted sha256=%x length=%d> refreshed=%t expiry=%s email=%s\n",
		digest[:6], len(credentials.AccessToken), credentials.Refreshed,
		credentials.Expiry.UTC().Format(time.RFC3339), credentials.Email)
}
