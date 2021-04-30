package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/vinser/flibgo/pkg/config"
	"github.com/vinser/flibgo/pkg/database"
	"github.com/vinser/flibgo/pkg/genres"
	"github.com/vinser/flibgo/pkg/opds"
	"github.com/vinser/flibgo/pkg/stock"

	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

func main() {
	const configFile = "config/config.yml"

	cfg := config.GetConfig(configFile)
	config.LoadLocales()
	langTag := language.Make(cfg.Language.DEFAULT)

	stockLog := config.NewLog(cfg.Logs.SCAN, cfg.Logs.DEBUG)
	defer stockLog.File.Close()
	opdsLog := config.NewLog(cfg.Logs.OPDS, cfg.Logs.DEBUG)
	defer opdsLog.File.Close()

	db := database.NewDB(cfg.Database.DSN)
	defer db.Close()
	if !db.IsReady() {
		db.InitDB(cfg.Database.INIT_SCRIPT)
		f := "Book stock was inited. Tables were created in empty database"
		stockLog.I.Println(f)
		return
	}

	genresTree := genres.NewGenresTree(cfg.Genres.TREE_FILE)

	stockHandler := &stock.Handler{
		CFG: cfg,
		LOG: stockLog,
		DB:  db,
		GT:  genresTree,
	}

	// Empty book stock database and then scan book stock directory to add books in book stock database
	reindex := flag.Bool("reindex", false, "empty book stock database and then scan book stock directory to add books in book stock database")
	flag.Parse()
	if *reindex {
		stockHandler.Reindex()
		return
	}

	stopScan := make(chan struct{})
	go func() {
		defer func() { stopScan <- struct{}{} }()
		f := "new aquisitions scanning started...\n"
		stockLog.I.Printf(f)
		log.Print(f)
		for {
			stockHandler.ScanDir(false)
			time.Sleep(time.Duration(cfg.Library.POLL_PERIOD) * time.Second)
			select {
			case <-stopScan:
				return
			default:
				continue
			}
		}
	}()

	opdsHandler := &opds.Handler{
		CFG: cfg,
		LOG: opdsLog,
		DB:  db,
		GT:  genresTree,
		P:   message.NewPrinter(langTag),
	}
	portString := fmt.Sprint(":", cfg.OPDS.PORT)
	server := &http.Server{
		Addr:    portString,
		Handler: opdsHandler,
	}
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %s\n", err)
		}
	}()
	f := "server on http://localhost%s is listening...\n"
	opdsLog.I.Printf(f, portString)
	log.Printf(f, portString)

	shutdown := make(chan os.Signal, 1)
	signal.Notify(shutdown, os.Interrupt, syscall.SIGINT, syscall.SIGTERM)
	<-shutdown
	f = "shutdown started...\n"
	opdsLog.I.Printf(f)
	log.Printf(f)

	// Stop scanning for new aquisitions and wait for completion
	stopScan <- struct{}{}
	<-stopScan
	f = "new aquisitions scanning was stoped successfully\n"
	stockLog.I.Printf(f)
	log.Printf(f)

	// Shutdown OPDS server
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		f := "shutdown error: %v\n"
		opdsLog.E.Printf(f, err)
		log.Fatalf(f, err)
	}
	f = "server on http://localhost%s was shut down successfully\n"
	opdsLog.I.Printf(f, portString)
	log.Printf(f, portString)
}
