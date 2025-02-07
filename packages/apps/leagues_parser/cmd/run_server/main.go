package main

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"sync"
	"sync/atomic"

	"cloud.google.com/go/pubsub"
	"gorm.io/gorm"
	"riccardotornesello.it/sharedtelemetry/iracing/cloudrun_utils/handlers"
	"riccardotornesello.it/sharedtelemetry/iracing/events_models"
	"riccardotornesello.it/sharedtelemetry/iracing/gorm_utils/database"
	"riccardotornesello.it/sharedtelemetry/iracing/leagues_parser/logic"
)

var db *gorm.DB
var pubSubTopic *pubsub.Topic
var pubSubCtx context.Context

func main() {
	var err error

	// Get configuration
	dbUser := os.Getenv("DB_USER")
	dbPass := os.Getenv("DB_PASS")
	dbName := os.Getenv("DB_NAME")
	dbPort := os.Getenv("DB_PORT")
	dbHost := os.Getenv("DB_HOST")

	pubSubProjectId := os.Getenv("PUBSUB_PROJECT")
	pubSubTopicId := os.Getenv("PUBSUB_TOPIC")

	// Initialize database
	db, err = database.Connect(dbUser, dbPass, dbHost, dbPort, dbName, events_models.AllModels, 1, 0)
	if err != nil {
		log.Fatalf("database.Connect: %v", err)
	}

	// Initialize pubsub
	pubSubCtx = context.Background()
	client, err := pubsub.NewClient(pubSubCtx, pubSubProjectId)
	if err != nil {
		log.Fatalf("pubsub.NewClient: %v", err)
	}
	defer client.Close()

	pubSubTopic = client.Topic(pubSubTopicId)

	// Start the HTTP server
	http.HandleFunc("/", PubSubHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
		log.Printf("Defaulting to port %s", port)
	}

	listener, err := net.Listen("tcp4", ":"+port)
	if err != nil {
		log.Fatal("Error starting server:", err)
	}

	log.Println("Listening on", listener.Addr())
	if err := http.Serve(listener, nil); err != nil {
		log.Fatal(err)
	}
}

func PubSubHandler(w http.ResponseWriter, r *http.Request) {
	seasonInfos, err := logic.GetActiveLeagueSeasonIds(db)
	if err != nil {
		handlers.ReturnException(w, err, "logic.GetActiveLeagueSeasonIds")
		return
	}

	// Send pub/sub messages to parse the season
	var wg sync.WaitGroup
	var totalErrors uint64

	for _, season := range seasonInfos {
		result := pubSubTopic.Publish(pubSubCtx, &pubsub.Message{
			Data: []byte("{\"leagueId\":" + strconv.Itoa(season.LeagueId) + ",\"seasonId\":" + strconv.Itoa(season.SeasonId) + "}"),
		})

		wg.Add(1)
		go func(res *pubsub.PublishResult) {
			defer wg.Done()
			_, err := res.Get(pubSubCtx)
			if err != nil {
				atomic.AddUint64(&totalErrors, 1)
				return
			}
		}(result)
	}

	wg.Wait()

	if totalErrors > 0 {
		handlers.ReturnException(w, fmt.Errorf("%d of %d messages did not publish successfully", totalErrors, len(seasonInfos)), "PubSubHandler")
		return
	}

	w.WriteHeader(http.StatusOK)
	return
}
