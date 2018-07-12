package main

import (
	"fmt"
	"log"
	"encoding/json"
	"github.com/gorilla/mux"
	"github.com/gorilla/handlers"
	"net/http"
	"strconv"
	mpd_handler "github.com/randomcoww/go-mpd-es/mpd_handler"
	es_handler "github.com/randomcoww/go-mpd-es/es_handler"
)

var (
	esIndex, esDocument = "songs", "song"
	mpdClient *mpd_handler.MpdClient
	esClient *es_handler.EsClient
)

type response struct {
	Message string
}


func NewServer(listenUrl, mpdUrl, esUrl string) {
	allowedHeaders := handlers.AllowedHeaders([]string{"X-Requested-With"})
	allowedOrigins := handlers.AllowedOrigins([]string{"*"})
	allowedMethods := handlers.AllowedMethods([]string{"GET", "HEAD", "POST", "PUT", "DELETE", "OPTIONS"})

	r := mux.NewRouter()

	r.HandleFunc("/healthcheck", healthCheck).
		Methods("GET")

	r.HandleFunc("/playlist/items", querytPlaylistItems).
		Queries("start", "{start}").
		Queries("end", "{end}").
		Methods("GET")

	r.HandleFunc("/database/search", search).
		Queries("q", "{query}").
		Methods("GET")

	r.HandleFunc("/playlist/items", movePlaylistItems).
		Queries("start", "{start}").
		Queries("end", "{end}").
		Queries("pos", "{moveto}").
		Methods("PUT")

	r.HandleFunc("/playlist/items", deletePlaylistItems).
		Queries("start", "{start}").
		Queries("end", "{end}").
		Methods("DELETE")

	r.HandleFunc("/playlist", addToPlaylist).
		Queries("path", "{path}").
		Methods("PUT")

	r.HandleFunc("/playlist", clearPlaylist).
		Methods("DELETE")

	mpdClient = mpd_handler.NewMpdClient("tcp", mpdUrl)
	esClient = es_handler.NewEsClient(esUrl, esIndex, esDocument, "")

	<-mpdClient.Up
	<-esClient.Up

	fmt.Printf("API server start on %s\n", listenUrl)
	log.Fatal(http.ListenAndServe(listenUrl, handlers.CORS(allowedHeaders, allowedOrigins, allowedMethods)(r)))
}


func parseNum(input string) (int) {
	v, err := strconv.Atoi(input)
	if err != nil {
		fmt.Printf("Error parsing param %s: %s\n", input, err)
		v = -1
	}

	return v
}


//
// handle func
//

func healthCheck(w http.ResponseWriter, r *http.Request) {
	fmt.Printf("Healthcheck\n")
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response{"ok"})
}


func querytPlaylistItems(w http.ResponseWriter, r *http.Request) {
	params := mux.Vars(r)
	fmt.Printf("Query playlist items %s\n", params)

	attrs, err := mpdClient.QueryPlaylistItems(
		parseNum(params["start"]),
		parseNum(params["end"]))
	w.Header().Set("Content-Type", "application/json")

	if err == nil {
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(attrs)
	} else {
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(response{err.Error()})
	}
}


func movePlaylistItems(w http.ResponseWriter, r *http.Request) {
	params := mux.Vars(r)
	fmt.Printf("Move playlist items %s\n", params)

	err := mpdClient.MovePlaylistItems(
		parseNum(params["start"]),
		parseNum(params["end"]),
		parseNum(params["moveto"]))
	w.Header().Set("Content-Type", "application/json")

	if err == nil {
		w.WriteHeader(http.StatusNoContent)
	} else {
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(response{err.Error()})
	}
}


func addToPlaylist(w http.ResponseWriter, r *http.Request) {
	params := mux.Vars(r)
	fmt.Printf("Add to playlist %s\n", params)

	err := mpdClient.AddToPlaylist(
		params["path"])
	w.Header().Set("Content-Type", "application/json")

	if err == nil {
		w.WriteHeader(http.StatusNoContent)
	} else {
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(response{err.Error()})
	}
}


func deletePlaylistItems(w http.ResponseWriter, r *http.Request) {
	params := mux.Vars(r)
	fmt.Printf("Delete playlist items %s\n", params)

	err := mpdClient.DeletePlaylistItems(
		parseNum(params["start"]),
		parseNum(params["end"]))
	w.Header().Set("Content-Type", "application/json")

	if err == nil {
		w.WriteHeader(http.StatusNoContent)
	} else {
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(response{err.Error()})
	}
}


func clearPlaylist(w http.ResponseWriter, r *http.Request) {
	fmt.Printf("Clear playlist items\n")

	err := mpdClient.ClearPlaylist()
	w.Header().Set("Content-Type", "application/json")

	if err == nil {
		w.WriteHeader(http.StatusNoContent)
	} else {
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(response{err.Error()})
	}
}


func search(w http.ResponseWriter, r *http.Request) {
	params := mux.Vars(r)
	fmt.Printf("Search database %s\n", params)

	search, err := esClient.Search(params["query"])
	w.Header().Set("Content-Type", "application/json")

	if err == nil {
		w.WriteHeader(http.StatusOK)

		var	result []*json.RawMessage
		for _, hits := range search.Hits.Hits {
			result = append(result, hits.Source)
		}

		json.NewEncoder(w).Encode(result)
	} else {
		w.WriteHeader(http.StatusUnprocessableEntity)
		json.NewEncoder(w).Encode(response{err.Error()})
	}
}
