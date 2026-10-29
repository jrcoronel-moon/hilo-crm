// Command hiloctl is the operator CLI, run inside the web container:
//
//	docker compose exec web hiloctl superadmin grant ana@example.com
//	docker compose exec web hiloctl superadmin revoke ana@example.com
//	docker compose exec web hiloctl superadmin list
//	docker compose exec web hiloctl domain allow example.com
//	docker compose exec web hiloctl domain deny example.com
//	docker compose exec web hiloctl domain list
//
// It uses the same DATABASE_URL (app role) as the service.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/google/uuid"

	"github.com/jrcoronel-moon/hilo-crm/internal/db"
	"github.com/jrcoronel-moon/hilo-crm/internal/identity"
)

const usage = `usage:
  hiloctl superadmin grant|revoke <email>
  hiloctl superadmin list
  hiloctl domain allow|deny <domain>
  hiloctl domain list`

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return errors.New(usage)
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is required")
	}
	d, err := db.Open(ctx, url)
	if err != nil {
		return err
	}
	defer d.Close()
	id := identity.New(d, slog.New(slog.DiscardHandler), nil)
	arg := func() (string, error) {
		if len(args) < 3 {
			return "", errors.New(usage)
		}
		return args[2], nil
	}
	switch args[0] + " " + args[1] {
	case "superadmin grant", "superadmin revoke":
		email, err := arg()
		if err != nil {
			return err
		}
		if err := id.SetSuperadmin(ctx, email, args[1] == "grant"); err != nil {
			return err
		}
		fmt.Printf("superadmin %s: %s\n", args[1], email)
	case "superadmin list":
		users, err := d.Platform().ListSuperadmins(ctx)
		if err != nil {
			return err
		}
		for _, u := range users {
			fmt.Println(u.Email)
		}
	case "domain allow":
		dom, err := arg()
		if err != nil {
			return err
		}
		if err := id.AddPlatformDomain(ctx, true, uuid.Nil, dom); err != nil {
			return err
		}
		fmt.Println("allowed:", dom)
	case "domain deny":
		dom, err := arg()
		if err != nil {
			return err
		}
		if err := id.RemovePlatformDomain(ctx, true, dom); err != nil {
			return err
		}
		fmt.Println("removed:", dom)
	case "domain list":
		doms, err := id.PlatformDomains(ctx, true)
		if err != nil {
			return err
		}
		for _, x := range doms {
			fmt.Println(x.Domain)
		}
	default:
		return errors.New(usage)
	}
	return nil
}
