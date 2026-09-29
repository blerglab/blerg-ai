package main

import (
	"fmt"
	"log"

	webpush "github.com/SherClockHolmes/webpush-go"
)

func main() {
	priv, pub, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		log.Fatalf("generate VAPID keys: %v", err)
	}
	fmt.Printf("VAPID_PRIVATE_KEY=%s\n", priv)
	fmt.Printf("VAPID_PUBLIC_KEY=%s\n", pub)
	fmt.Println()
	fmt.Println("Add these to your secret:")
	fmt.Printf("  make secret DAEMON_TOKEN=... DATABASE_URL=... VAPID_PUBLIC_KEY=%s VAPID_PRIVATE_KEY=%s\n", pub, priv)
}
