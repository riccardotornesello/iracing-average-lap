package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"

	"gorm.io/gorm"
	irapi "riccardotornesello.it/sharedtelemetry/iracing/iracing-api"
	"riccardotornesello.it/sharedtelemetry/iracing/logic"
)

var db *gorm.DB
var irClient *irapi.IRacingApiClient

func main() {
	var err error

	db, irClient, err = logic.InitHandler()
	if err != nil {
		log.Fatal(err)
	}

	http.HandleFunc("/", PubSubHandler)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
		log.Printf("Defaulting to port %s", port)
	}

	log.Printf("Listening on port %s", port)
	if err := http.ListenAndServe(":"+port, nil); err != nil {
		log.Fatal(err)
	}
}

type PubSubMessage struct {
	Message struct {
		Data []byte `json:"data,omitempty"`
		ID   string `json:"id"`
	} `json:"message"`
	Subscription string `json:"subscription"`
}

type SeasonData struct {
	LeagueId int `json:"leagueId"`
	SeasonId int `json:"seasonId"`
}

func PubSubHandler(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	defer r.Body.Close()
	if err != nil {
		log.Printf("io.ReadAll: %v", err)
		w.WriteHeader(http.StatusOK)
		return
	}

	var m PubSubMessage
	if err := json.Unmarshal(body, &m); err != nil {
		log.Printf("json.Unmarshal: %v", err)
		w.WriteHeader(http.StatusOK)
		return
	}

	var seasonData SeasonData
	if err := json.Unmarshal(m.Message.Data, &seasonData); err != nil {
		log.Printf("json.Unmarshal data: %v", err)
		w.WriteHeader(http.StatusOK)
		return
	}

	if err := logic.ParseLeague(seasonData.LeagueId, seasonData.SeasonId, irClient, db); err != nil {
		log.Printf("logic.ParseLeague: %v", err)
		w.WriteHeader(http.StatusOK)
		return
	}

	w.WriteHeader(http.StatusOK)

	return
}