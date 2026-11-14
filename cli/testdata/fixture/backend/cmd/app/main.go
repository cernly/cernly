package main

import (
	"fmt"
	"os"

	"example.com/backend/internal/billing"
	"example.com/backend/internal/store"
)

func main() {
	fmt.Println(os.Getenv("DATABASE_URL"), os.Getenv("MOLLIE_API_URL"))
	billing.Charge(store.Open())
}
