// adduser creates a dispatcher account, or resets an existing one's name and
// password. There's no sign-up page on purpose: accounts are made here, by
// someone with database access.
//
//	go run ./cmd/adduser -email ada@example.com -name "Ada Obi"
//
// The password is read from the DISPATCHER_PASSWORD environment variable if
// set, otherwise from the first line of standard input.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/joho/godotenv"

	"github.com/WllowyEmmie/trucking-logistics/internal/auth"
	"github.com/WllowyEmmie/trucking-logistics/internal/db"
)

func main() {
	_ = godotenv.Load()
	email := flag.String("email", "", "dispatcher email (their sign-in)")
	name := flag.String("name", "", "name shown in the console and on decisions")
	flag.Parse()
	if *email == "" || *name == "" {
		flag.Usage()
		os.Exit(2)
	}

	pw := os.Getenv("DISPATCHER_PASSWORD")
	if pw == "" {
		fmt.Fprint(os.Stderr, "Password (at least 10 characters): ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && line == "" {
			log.Fatal("no password given")
		}
		pw = strings.TrimRight(line, "\r\n")
	}
	if err := auth.ValidPassword(pw); err != nil {
		log.Fatal(err)
	}
	hash, err := auth.HashSecret(pw)
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is not set")
	}
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer pool.Close()

	d, err := db.New(pool).UpsertDispatcher(ctx, strings.TrimSpace(*email), strings.TrimSpace(*name), hash)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("dispatcher %s (%s) can sign in\n", d.Email, d.Name)
}
