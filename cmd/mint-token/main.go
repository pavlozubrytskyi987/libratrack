package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"libratrack/internal/auth"
)

func main() {
	sub := flag.String("sub", "demo-user", "subject")
	role := flag.String("role", auth.RoleLibrarian, "role: reader | librarian")
	ttl := flag.Duration("ttl", time.Hour, "token TTL")
	secret := flag.String("secret", envOr("JWT_SECRET", auth.DefaultDemoSecret), "HMAC secret (must match server)")
	flag.Parse()

	if *role != auth.RoleReader && *role != auth.RoleLibrarian {
		log.Fatalf("unknown role %q (allowed: reader, librarian)", *role)
	}

	tok, err := auth.Sign([]byte(*secret), auth.Claims{
		Subject:   *sub,
		Role:      *role,
		ExpiresAt: time.Now().Add(*ttl).Unix(),
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(tok)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
