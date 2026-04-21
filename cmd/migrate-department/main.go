package main

import (
	"log"

	"github.com/automax/backend/internal/config"
	"github.com/automax/backend/internal/database"
	"github.com/automax/backend/migrations"
)

func main() {

	cfgs := &config.DatabaseConfig{
		Host:     "localhost",
		User:     "automax",
		Password: "automax123",
		DBName:   "corteza_automax",
		Port:     "5432",
		SSLMode:  "disable",
	}

	cfg := config.Load()

	srcDB, err := database.ConnectCorteza(cfgs)
	if err != nil {
		log.Fatal(err)
	}

	destDB, err := database.Connect(&cfg.Database)
	if err != nil {
		log.Fatal(err)
	}

	err = migrations.MigrateDepartments(srcDB, destDB)
	if err != nil {
		log.Fatal(err)
	}

	log.Println("✅ Department migration completed")
}
