package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
	"sort"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/joho/godotenv"
	"riccardotornesello.it/sharedtelemetry/iracing/api/logic"
	"riccardotornesello.it/sharedtelemetry/iracing/gorm_utils/database"
)

type CacheHit struct {
	data       []byte
	validUntil time.Time
}

type RankingResponse struct {
	Ranking     []*Rank             `json:"ranking"`
	Drivers     map[int]*DriverInfo `json:"drivers"`
	EventGroups []*EventGroupInfo   `json:"eventGroups"`
	Competition *CompetitionInfo    `json:"competition"`
}

type Rank struct {
	Pos     int                     `json:"pos"`
	CustId  int                     `json:"custId"`
	Sum     int                     `json:"sum"`
	Results map[uint]map[string]int `json:"results"`
}

type TeamInfo struct {
	Id      uint   `json:"id"`
	Name    string `json:"name"`
	Picture string `json:"picture"`
}

type CrewInfo struct {
	Id              uint     `json:"id"`
	Name            string   `json:"name"`
	CarId           int      `json:"carId"`
	Team            TeamInfo `json:"team"`
	CarBrandPicture string   `json:"carBrandPicture"`
}

type DriverInfo struct {
	CustId int      `json:"custId"`
	Name   string   `json:"name"`
	Crew   CrewInfo `json:"crew"`
}

type EventGroupInfo struct {
	Id      uint     `json:"id"`
	Name    string   `json:"name"`
	TrackId int      `json:"trackId"`
	Dates   []string `json:"dates"`
}

type CompetitionInfo struct {
	Id               uint   `json:"id"`
	Name             string `json:"name"`
	CrewDriversCount int    `json:"crewDriversCount"`
}

func main() {
	// Get configuration
	err := godotenv.Load()
	if err != nil {
		log.Println("Error loading .env file")
	}

	dbUser := os.Getenv("DB_USER")
	dbPass := os.Getenv("DB_PASS")
	dbName := os.Getenv("DB_NAME")
	dbPort := os.Getenv("DB_PORT")
	dbHost := os.Getenv("DB_HOST")

	// Initialize database
	db, err := database.Connect(dbUser, dbPass, dbHost, dbPort, dbName, 1, 1)
	if err != nil {
		log.Fatal(err)
	}

	r := gin.Default()

	// Initialize cache
	cache := make(map[string]CacheHit)

	// Handlers
	r.GET("/competitions/:id/ranking", func(c *gin.Context) {
		// Check if the response is in the cache
		if cacheHit, ok := cache[c.Request.RequestURI]; ok {
			if cacheHit.validUntil.After(time.Now()) {
				c.Data(http.StatusOK, "application/json", cacheHit.data)
				return
			}
		}

		// Get the competition
		competition, err := logic.GetCompetitionBySlug(db, c.Param("id"))

		// Get the sessions valid for the competition
		sessions, sessionsMap, err := logic.GetCompetitionSessions(db, competition.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Error getting competition sessions"})
			return
		}

		// Get event groups
		eventGroups, err := logic.GetEventGroups(db, competition.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Error getting event groups"})
			return
		}

		// Get drivers
		drivers, _, err := logic.GetCompetitionDrivers(db, competition.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Error getting competition drivers"})
			return
		}

		driverCars := make(map[int]int)
		for _, driver := range drivers {
			driverCars[driver.IRacingCustId] = driver.Crew.IRacingCarId
		}

		// Get laps
		var simsessionIds [][]int
		for _, session := range sessions {
			simsessionIds = append(simsessionIds, []int{session.SubsessionId, session.SimsessionNumber})
		}

		laps, err := logic.GetLaps(db, simsessionIds)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Error getting laps"})
			return
		}

		// Analyze
		allResults := make(map[int]map[int]int)
		bestResults := make(map[int]map[uint]map[string]int) // Customer ID, Group, Date, average ms

		currentCustId := 0
		currentSubsessionId := 0
		stintEnd := false
		stintValidLaps := 0
		stintTimeSum := 0

		for _, lap := range laps {
			// Check the first key of driverResults
			if lap.CustID != currentCustId {
				allResults[lap.CustID] = make(map[int]int)
				currentCustId = lap.CustID
				stintEnd = false
				stintValidLaps = 0
				stintTimeSum = 0
			}

			if lap.SubsessionID != currentSubsessionId {
				allResults[lap.CustID][lap.SubsessionID] = 0
				currentSubsessionId = lap.SubsessionID
				stintEnd = false
				stintValidLaps = 0
				stintTimeSum = 0
			}

			driverCar, ok := driverCars[lap.CustID]
			if !ok {
				continue
			}

			if driverCar != lap.SessionSimsessionParticipant.CarID {
				continue
			}

			if stintEnd {
				continue
			}

			if logic.IsLapPitted(lap.LapEvents) {
				if stintValidLaps > 0 {
					stintEnd = true
				}

				continue
			}

			if logic.IsLapValid(lap.LapNumber, lap.LapTime, lap.LapEvents, lap.Incident) {
				stintValidLaps++
				stintTimeSum += lap.LapTime

				if stintValidLaps == 3 {
					stintEnd = true

					averageTime := stintTimeSum / 3 / 10

					// Store the average time of the session for the driver (only valid stints)
					allResults[lap.CustID][lap.SubsessionID] = averageTime

					// Store the best result of the driver for the date in the event group (only valid stints)
					sessionDetails := sessionsMap[lap.SubsessionID]
					// 1. Add the customer to the map if it does not exist
					if _, ok := bestResults[lap.CustID]; !ok {
						bestResults[lap.CustID] = make(map[uint]map[string]int)
					}
					// 2. Add the event group to the map if it does not exist
					if _, ok := bestResults[lap.CustID][sessionDetails.EventGroupId]; !ok {
						bestResults[lap.CustID][sessionDetails.EventGroupId] = make(map[string]int)
					}
					// 3. Add the result to the date if it does not exist or if it is better than the previous one
					if oldResult, ok := bestResults[lap.CustID][sessionDetails.EventGroupId][sessionDetails.Date]; !ok {
						bestResults[lap.CustID][sessionDetails.EventGroupId][sessionDetails.Date] = averageTime
					} else {
						if oldResult > averageTime {
							bestResults[lap.CustID][sessionDetails.EventGroupId][sessionDetails.Date] = averageTime
						}
					}
				}
			} else {
				stintValidLaps = 0
				stintEnd = true
			}
		}

		// Generate the ranking
		ranking := make([]*Rank, 0)
		for _, driver := range drivers {
			driverRank := &Rank{
				CustId:  driver.IRacingCustId,
				Sum:     0,
				Results: bestResults[driver.IRacingCustId], // TODO: add default value, it might be null
			}

			driverBestResults, ok := bestResults[driver.IRacingCustId]
			if !ok {
				ranking = append(ranking, driverRank)
				continue
			}

			sum := 0
			isValid := true
			for _, eventGroup := range eventGroups {
				if driverBestGroupResults, ok := driverBestResults[eventGroup.ID]; !ok {
					// If the driver did not participate in the event group, the result is 0
					isValid = false
					break
				} else {
					// Check if the driver has at least a result in one date of the event group and in case add the best result
					bestResult := 0
					for _, result := range driverBestGroupResults {
						if bestResult == 0 || result < bestResult {
							bestResult = result
						}
					}

					if bestResult > 0 {
						sum += bestResult
					} else {
						isValid = false
						break
					}
				}
			}

			if isValid {
				driverRank.Sum = sum
			}

			ranking = append(ranking, driverRank)
		}

		// Sort the ranking by sum. If the sum is 0, put the driver at the end of the ranking
		sort.Slice(ranking, func(i, j int) bool {
			if ranking[i].Sum == 0 {
				return false
			}

			if ranking[j].Sum == 0 {
				return true
			}

			return ranking[i].Sum < ranking[j].Sum
		})

		for i, driver := range ranking {
			driver.Pos = i + 1
		}

		// Return the response
		driversInfo := make(map[int]*DriverInfo)
		for _, driver := range drivers {
			driverInfo := &DriverInfo{
				CustId: driver.IRacingCustId,
				Name:   driver.Name,
				Crew: CrewInfo{
					Id:              driver.Crew.ID,
					Name:            driver.Crew.Name,
					CarId:           driver.Crew.IRacingCarId,
					CarBrandPicture: driver.Crew.CarBrandPicture,
					Team: TeamInfo{
						Id:      driver.Crew.Team.ID,
						Name:    driver.Crew.Team.Name,
						Picture: driver.Crew.Team.Picture,
					},
				},
			}

			driversInfo[driver.IRacingCustId] = driverInfo
		}

		eventGroupsInfo := make([]*EventGroupInfo, 0)
		for _, eventGroup := range eventGroups {
			eventGroupInfo := &EventGroupInfo{
				Id:      eventGroup.ID,
				Name:    eventGroup.Name,
				TrackId: eventGroup.IRacingTrackId,
				Dates:   eventGroup.Dates,
			}

			eventGroupsInfo = append(eventGroupsInfo, eventGroupInfo)
		}

		competitionInfo := &CompetitionInfo{
			Id:               competition.ID,
			Name:             competition.Name,
			CrewDriversCount: competition.CrewDriversCount,
		}

		response := RankingResponse{
			Ranking:     ranking,
			EventGroups: eventGroupsInfo,
			Drivers:     driversInfo,
			Competition: competitionInfo,
		}

		// Marshal the response
		jsonString, err := json.Marshal(response)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Error marshalling response"})
			return
		}

		// Store the response in the cache
		cache[c.Request.RequestURI] = CacheHit{
			data:       jsonString,
			validUntil: time.Now().Add(5 * time.Minute),
		}

		// Return the response
		c.Data(http.StatusOK, "application/json", jsonString)
	})

	r.GET("/competitions/:id/csv", func(c *gin.Context) {
		// Get the competition
		competition, err := logic.GetCompetitionBySlug(db, c.Param("id"))

		// Get the sessions valid for the competition
		sessions, sessionsMap, err := logic.GetCompetitionSessions(db, competition.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Error getting competition sessions"})
			return
		}

		// Get drivers
		drivers, _, err := logic.GetCompetitionDrivers(db, competition.ID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Error getting competition drivers"})
			return
		}

		driverCars := make(map[int]int)
		for _, driver := range drivers {
			driverCars[driver.IRacingCustId] = driver.Crew.IRacingCarId
		}

		// Get laps
		var simsessionIds [][]int
		for _, session := range sessions {
			simsessionIds = append(simsessionIds, []int{session.SubsessionId, session.SimsessionNumber})
		}

		laps, err := logic.GetLaps(db, simsessionIds)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Error getting laps"})
			return
		}

		// Analyze
		allResults := make(map[int]map[int]int)
		bestResults := make(map[int]map[uint]map[string]int) // Customer ID, Group, Date, average ms

		currentCustId := 0
		currentSubsessionId := 0
		stintEnd := false
		stintValidLaps := 0
		stintTimeSum := 0

		for _, lap := range laps {
			// Check the first key of driverResults
			if lap.CustID != currentCustId {
				allResults[lap.CustID] = make(map[int]int)
				currentCustId = lap.CustID
				stintEnd = false
				stintValidLaps = 0
				stintTimeSum = 0
			}

			if lap.SubsessionID != currentSubsessionId {
				allResults[lap.CustID][lap.SubsessionID] = 0
				currentSubsessionId = lap.SubsessionID
				stintEnd = false
				stintValidLaps = 0
				stintTimeSum = 0
			}

			driverCar, ok := driverCars[lap.CustID]
			if !ok {
				continue
			}

			if driverCar != lap.SessionSimsessionParticipant.CarID {
				continue
			}

			if stintEnd {
				continue
			}

			if logic.IsLapPitted(lap.LapEvents) {
				if stintValidLaps > 0 {
					stintEnd = true
				}

				continue
			}

			if logic.IsLapValid(lap.LapNumber, lap.LapTime, lap.LapEvents, lap.Incident) {
				stintValidLaps++
				stintTimeSum += lap.LapTime

				if stintValidLaps == 3 {
					stintEnd = true

					averageTime := stintTimeSum / 3 / 10

					// Store the average time of the session for the driver (only valid stints)
					allResults[lap.CustID][lap.SubsessionID] = averageTime

					// Store the best result of the driver for the date in the event group (only valid stints)
					sessionDetails := sessionsMap[lap.SubsessionID]
					// 1. Add the customer to the map if it does not exist
					if _, ok := bestResults[lap.CustID]; !ok {
						bestResults[lap.CustID] = make(map[uint]map[string]int)
					}
					// 2. Add the event group to the map if it does not exist
					if _, ok := bestResults[lap.CustID][sessionDetails.EventGroupId]; !ok {
						bestResults[lap.CustID][sessionDetails.EventGroupId] = make(map[string]int)
					}
					// 3. Add the result to the date if it does not exist or if it is better than the previous one
					if oldResult, ok := bestResults[lap.CustID][sessionDetails.EventGroupId][sessionDetails.Date]; !ok {
						bestResults[lap.CustID][sessionDetails.EventGroupId][sessionDetails.Date] = averageTime
					} else {
						if oldResult > averageTime {
							bestResults[lap.CustID][sessionDetails.EventGroupId][sessionDetails.Date] = averageTime
						}
					}
				}
			} else {
				stintValidLaps = 0
				stintEnd = true
			}
		}

		// Generate CSV
		csv := logic.GenerateSessionsCsv(sessions, drivers, allResults)

		c.Header("Content-Description", "File Transfer")
		c.Header("Content-Disposition", "attachment; filename=sessions.csv")
		c.Data(http.StatusOK, "text/csv", []byte(csv))
	})

	r.Run()
}
