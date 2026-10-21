package main

import (
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"d2c-order-followup-automation/internal/httpapi"
	"d2c-order-followup-automation/internal/storage"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "3000"
	}
	databasePath := os.Getenv("DB_PATH")
	if databasePath == "" {
		databasePath = filepath.Join("..", "data", "orders.sqlite")
	}
	store, err := storage.OpenStore(databasePath, filepath.Join("..", "data", "store.json"))
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	address := ":" + port
	log.Printf("D2C order follow-up server listening at http://localhost%s", address)
	server := &http.Server{Addr: address, Handler: httpapi.NewServer(store), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	log.Fatal(server.ListenAndServe())
}
