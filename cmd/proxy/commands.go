package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"antigravity-go-proxy/internal/accounts"
	"antigravity-go-proxy/internal/auth"
)

func cmdStatus(listenAddr string) error {
	pid, err := ReadPIDFile()
	if err != nil || !IsProcessRunning(pid) {
		fmt.Println("Status: Stopped (not running)")
		return nil
	}

	fmt.Printf("Status: Running (PID: %d)\n", pid)
	baseURL := fmt.Sprintf("http://%s", formatAddr(listenAddr))

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(baseURL + "/account-limits?format=table")
	if err != nil {
		fmt.Printf("Warning: Unable to query proxy API at %s: %v\n", baseURL, err)
		return nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	fmt.Println("\nAccount Pool Status:")
	fmt.Println(string(body))
	return nil
}

func cmdWeb(listenAddr string) error {
	url := fmt.Sprintf("http://%s", formatAddr(listenAddr))
	fmt.Printf("Opening %s in default browser...\n", url)
	return auth.OpenBrowser(url)
}

func cmdAccounts(args []string) error {
	if len(args) == 0 {
		return cmdAccountsList()
	}

	sub := args[0]
	switch sub {
	case "list":
		return cmdAccountsList()
	case "add":
		return cmdAccountsAdd()
	case "remove":
		if len(args) < 2 {
			return fmt.Errorf("usage: antigravity-proxy accounts remove <email>")
		}
		return cmdAccountsRemove(args[1])
	case "verify":
		return cmdAccountsVerify()
	default:
		return fmt.Errorf("unknown accounts subcommand: %s (available: list, add, remove, verify)", sub)
	}
}

func cmdAccountsList() error {
	mgr, err := accounts.NewDefault("", accounts.DefaultStrategy, nil)
	if err != nil {
		return fmt.Errorf("load accounts: %w", err)
	}
	accountsList := mgr.GetAllAccounts()
	if len(accountsList) == 0 {
		fmt.Println("No accounts configured.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "EMAIL\tSOURCE\tSTATUS\tTIER\tPROJECT ID")
	for _, acc := range accountsList {
		status := "enabled"
		if !acc.Enabled {
			status = "disabled"
		} else if acc.IsInvalid {
			status = "invalid"
		}
		tier := acc.Subscription.Tier
		if tier == "" {
			tier = "unknown"
		}
		projectID := acc.ProjectID
		if projectID == "" {
			projectID = "-"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", acc.Email, acc.Source, status, tier, projectID)
	}
	return w.Flush()
}

func cmdAccountsAdd() error {
	mgr, err := accounts.NewDefault("", accounts.DefaultStrategy, nil)
	if err != nil {
		return fmt.Errorf("load accounts: %w", err)
	}

	om := auth.NewOAuthManager(mgr)
	authURL, _, err := om.StartFlow()
	if err != nil {
		return fmt.Errorf("start OAuth flow: %w", err)
	}

	fmt.Println("Opening browser for Google OAuth sign-in...")
	fmt.Println("If the browser does not open automatically, visit:")
	fmt.Println(authURL)
	fmt.Println("\nWaiting for authentication callback on port 51121...")

	_ = auth.OpenBrowser(authURL)

	// Wait for callback to complete
	deadline := time.Now().Add(2 * time.Minute)
	initialCount := len(mgr.GetAllAccounts())
	for time.Now().Before(deadline) {
		time.Sleep(1 * time.Second)
		_ = mgr.Reload("")
		if len(mgr.GetAllAccounts()) > initialCount {
			fmt.Println("✅ Account added successfully!")
			return nil
		}
	}

	return fmt.Errorf("OAuth flow timed out after 2 minutes")
}

func cmdAccountsRemove(email string) error {
	mgr, err := accounts.NewDefault("", accounts.DefaultStrategy, nil)
	if err != nil {
		return fmt.Errorf("load accounts: %w", err)
	}
	if err := mgr.RemoveAccount(email); err != nil {
		return err
	}
	fmt.Printf("✅ Account %s removed.\n", email)
	return nil
}

func cmdAccountsVerify() error {
	mgr, err := accounts.NewDefault("", accounts.DefaultStrategy, nil)
	if err != nil {
		return fmt.Errorf("load accounts: %w", err)
	}
	resolver := accounts.NewCredentialResolver(auth.Manager{}, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	accountsList := mgr.GetAllAccounts()
	fmt.Printf("Verifying %d account(s)...\n", len(accountsList))
	for _, acc := range accountsList {
		creds, err := resolver.Resolve(ctx, acc)
		if err != nil {
			fmt.Printf("❌ %s: Failed (%v)\n", acc.Email, err)
		} else {
			fmt.Printf("✅ %s: Valid (Token length: %d)\n", acc.Email, len(creds.AccessToken))
		}
	}
	return nil
}

func formatAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "127.0.0.1" + addr
	}
	if strings.HasPrefix(addr, "0.0.0.0:") {
		return "127.0.0.1:" + strings.TrimPrefix(addr, "0.0.0.0:")
	}
	return addr
}
