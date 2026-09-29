package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"

	"github.com/blerglab/blerg-ai/core/internal/authprovider"
	"github.com/blerglab/blerg-ai/core/internal/db"
	"github.com/blerglab/blerg-ai/core/internal/identity"
)

// runUsers implements `blerg-core users create|set-password` (R8: admin recovery and minimal
// user management, without psql or a hand-made bcrypt hash). It prints exactly one line to
// stdout on success — "provider_subject=<S> password=<one-time>" — the one and only place the
// new password is ever available in plaintext, and returns the exit code the caller should use:
// 2 for a usage/argument error, 1 for a runtime failure (bad DSN, DB unreachable, unknown
// subject, ...), 0 on success.
func runUsers(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: blerg-core users create --subject S --role member|admin | set-password --subject S")
		return 2
	}
	switch args[0] {
	case "create", "set-password":
	default:
		fmt.Fprintf(os.Stderr, "users: unknown subcommand %q\n", args[0])
		return 2
	}

	fs := flag.NewFlagSet("users "+args[0], flag.ContinueOnError)
	subject := fs.String("subject", "", "provider_subject (the username)")
	role := fs.String("role", "member", "admin|member (create only)")
	if err := fs.Parse(args[1:]); err != nil || *subject == "" {
		fs.Usage()
		return 2
	}
	if args[0] == "create" && *role != "admin" && *role != "member" {
		fmt.Fprintln(os.Stderr, "users create: --role must be admin or member")
		return 2
	}

	ctx := context.Background()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Print("DATABASE_URL is required")
		return 1
	}
	st, err := db.Open(ctx, dsn)
	if err != nil {
		log.Printf("open store: %v", err)
		return 1
	}
	defer st.Close()

	local := authprovider.NewLocal(st)
	var pw string
	switch args[0] {
	case "create":
		pw, err = local.CreateLocalAccount(ctx, *subject, *role)
	case "set-password":
		kp, kerr := identity.LoadOrGenerateSigningKey(ctx, st)
		if kerr != nil {
			log.Printf("load signing key: %v", kerr)
			return 1
		}
		svc := identity.NewService(st, kp)
		pw, err = local.ResetPassword(ctx, *subject, svc.RevokeAccountEverywhere)
	}
	if err != nil {
		log.Printf("users %s: %v", args[0], err)
		return 1
	}
	_, _ = fmt.Fprintf(stdout, "provider_subject=%s password=%s\n", *subject, pw)
	return 0
}
